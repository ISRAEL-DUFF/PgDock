package edge

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/storage"
)

// Object is an object as the API shows it.
type Object struct {
	ID           uuid.UUID       `json:"id"`
	Bucket       string          `json:"bucket"`
	Path         string          `json:"path"`
	Size         int64           `json:"size"`
	MIMEType     string          `json:"mime_type"`
	ETag         string          `json:"etag"`
	Checksum     *string         `json:"checksum,omitempty"`
	Owner        *uuid.UUID      `json:"owner,omitempty"`
	UserMetadata json.RawMessage `json:"user_metadata"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	version      uuid.UUID
}

const objectCols = `id, bucket, path, version, size, mime_type, etag, checksum, owner, user_metadata, created_at, updated_at`

func scanObject(row pgx.Row) (*Object, error) {
	var o Object
	if err := row.Scan(&o.ID, &o.Bucket, &o.Path, &o.version, &o.Size, &o.MIMEType, &o.ETag, &o.Checksum, &o.Owner,
		&o.UserMetadata, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

// getObject reads bucket/path in q's transaction (as its role: policies
// decide whether the row is there); nil when it isn't.
func getObject(ctx context.Context, q pgx.Tx, bucket, path string) (*Object, error) {
	o, err := scanObject(q.QueryRow(ctx, `SELECT `+objectCols+` FROM pgd_storage.objects WHERE bucket = $1 AND path = $2`, bucket, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

// objectsGate refuses anon and user while pgd_storage.objects has no
// row-level security (an owner who turned it off would expose every file).
func objectsGate(ctx context.Context, tx pgx.Tx, role string) error {
	if role == "service" {
		return nil
	}
	var on bool
	if err := tx.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE oid = 'pgd_storage.objects'::regclass`).Scan(&on); err != nil {
		return err
	}
	if !on {
		return refuse(http.StatusForbidden, "rls_required", "pgd_storage.objects has no row-level security, so files can't be reached with the publishable key: turn it back on")
	}
	return nil
}

// asRole runs fn in a transaction as req's role, after the gate.
func (e *Edge) asRole(ctx context.Context, c *call, req Request, fn func(pgx.Tx) error) error {
	return e.WithRequest(ctx, c.p, req, func(tx pgx.Tx) error {
		if err := objectsGate(ctx, tx, req.Role); err != nil {
			return err
		}
		return fn(tx)
	})
}

var errObjectStore = errors.New("object store")

func storeErr(err error) error {
	if err == nil || errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidRange) {
		return err
	}
	return fmt.Errorf("%w: %w", errObjectStore, err)
}

// ---- Downloads -----------------------------------------------------------------

// inlineTypes are shown in the browser; anything else downloads.
var inlineTypes = regexp.MustCompile(`^(image/(png|jpeg|gif|webp|avif|bmp)|video/[a-z0-9.+-]+|audio/[a-z0-9.+-]+|text/plain|application/pdf)$`)

var rangeRe = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// serveObject streams o's bytes (or the range asked for) with its headers.
// public sets a shared cache's max-age; download forces an attachment
// (with that file name when it isn't "1" or "true").
func (e *Edge) serveObject(c *call, sc *edgeapi.StorageConfig, cl *storage.Client, o *Object, cacheSeconds int, public bool, download string) {
	if sc.EgressBlocked {
		c.w.Header().Set("Retry-After", "3600")
		c.fail(http.StatusTooManyRequests, "storage_egress_limit", "the organisation's file download allowance for this month is used up")
		return
	}
	h := c.w.Header()
	etag := `"` + o.ETag + `"`
	h.Set("ETag", etag)
	h.Set("Last-Modified", o.UpdatedAt.UTC().Format(http.TimeFormat))
	h.Set("Accept-Ranges", "bytes")
	h.Set("X-Content-Type-Options", "nosniff")
	// Files never run as the API's origin.
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	if public {
		h.Set("Cache-Control", "public, max-age="+strconv.Itoa(cacheSeconds))
	} else {
		h.Set("Cache-Control", "private, max-age="+strconv.Itoa(cacheSeconds))
	}
	name := pathBase(o.Path)
	if download != "" && download != "1" && download != "true" {
		name = download
	}
	disp := "inline"
	if download != "" || !inlineTypes.MatchString(o.MIMEType) {
		disp = "attachment"
	}
	h.Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(name))
	if inm := c.r.Header.Get("If-None-Match"); inm != "" && (inm == etag || inm == "*") {
		c.w.WriteHeader(http.StatusNotModified)
		return
	}
	rng := c.r.Header.Get("Range")
	if rng != "" {
		m := rangeRe.FindStringSubmatch(rng)
		if m == nil || (m[1] == "" && m[2] == "") {
			rng = "" // several ranges or a malformed one: the whole object
		}
	}
	if c.r.Method == http.MethodHead {
		h.Set("Content-Type", o.MIMEType)
		h.Set("Content-Length", strconv.FormatInt(o.Size, 10))
		c.w.WriteHeader(http.StatusOK)
		return
	}
	ctx := c.r.Context()
	obj, err := cl.Get(ctx, objectKey(sc, o.version), rng)
	if errors.Is(err, storage.ErrInvalidRange) {
		h.Set("Content-Range", "bytes */"+strconv.FormatInt(o.Size, 10))
		c.fail(http.StatusRequestedRangeNotSatisfiable, "invalid_range", "the range is outside the object")
		return
	}
	if errors.Is(err, storage.ErrNotFound) {
		e.cfg.Log.Warn("storage object without bytes", "project", c.p.cfg.Ref, "bucket", o.Bucket, "path", o.Path)
		c.fail(http.StatusNotFound, "object_not_found", "the object's data is missing")
		return
	}
	if err != nil {
		e.storageError(c, storeErr(err))
		return
	}
	defer obj.Body.Close()
	c.storage = true
	h.Set("Content-Type", o.MIMEType)
	h.Set("Content-Length", strconv.FormatInt(obj.Length, 10))
	status := http.StatusOK
	if rng != "" && obj.Range != "" {
		h.Set("Content-Range", obj.Range)
		status = http.StatusPartialContent
	}
	c.w.WriteHeader(status)
	_, _ = io.Copy(c.w, obj.Body)
}

func pathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// download serves an object to the caller its policies let read it.
func (e *Edge) download(c *call, req Request, bucket, path string) {
	sc, cl, ok := e.store(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), req.Timeout+3*time.Second)
	defer cancel()
	var o *Object
	err := e.asRole(ctx, c, req, func(tx pgx.Tx) error {
		var err error
		o, err = getObject(ctx, tx, bucket, path)
		return err
	})
	if err != nil {
		e.storageError(c, err)
		return
	}
	if o == nil {
		c.fail(http.StatusNotFound, "object_not_found", "no such object")
		return
	}
	b, err := e.bucket(ctx, c.p, bucket)
	if err != nil {
		e.storageError(c, err)
		return
	}
	e.serveObject(c, sc, cl, o, b.CacheSeconds, false, c.r.URL.Query().Get("download"))
}

// publicDownload serves an object of a public bucket to anyone.
func (e *Edge) publicDownload(c *call, bucket, path string) {
	sc, cl, ok := e.store(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), 10*time.Second)
	defer cancel()
	b, err := e.bucket(ctx, c.p, bucket)
	if errors.Is(err, errNoBucket) || (err == nil && !b.Public) {
		c.fail(http.StatusNotFound, "object_not_found", "no such object")
		return
	}
	if err != nil {
		e.storageError(c, err)
		return
	}
	o, err := e.edgeObject(ctx, c.p, bucket, path)
	if err != nil {
		e.storageError(c, err)
		return
	}
	if o == nil {
		c.fail(http.StatusNotFound, "object_not_found", "no such object")
		return
	}
	e.serveObject(c, sc, cl, o, b.CacheSeconds, true, c.r.URL.Query().Get("download"))
}

// edgeObject reads an object with the edge's own login (public buckets,
// signed URLs).
func (e *Edge) edgeObject(ctx context.Context, p *project, bucket, path string) (*Object, error) {
	pool, err := e.dbPool(ctx, p)
	if err != nil {
		return nil, err
	}
	o, err := scanObject(pool.QueryRow(ctx, `SELECT `+objectCols+` FROM pgd_storage.objects WHERE bucket = $1 AND path = $2`, bucket, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

// info answers an object's metadata.
func (e *Edge) info(c *call, req Request, bucket, path string) {
	ctx, cancel := context.WithTimeout(c.r.Context(), req.Timeout+3*time.Second)
	defer cancel()
	var o *Object
	err := e.asRole(ctx, c, req, func(tx pgx.Tx) error {
		var err error
		o, err = getObject(ctx, tx, bucket, path)
		return err
	})
	if err != nil {
		e.storageError(c, err)
		return
	}
	if o == nil {
		c.fail(http.StatusNotFound, "object_not_found", "no such object")
		return
	}
	c.json(http.StatusOK, o)
}

// ---- Uploads -------------------------------------------------------------------

// upload is what an upload stores, once its bytes are in.
type upload struct {
	bucket, path string
	version      uuid.UUID
	size         int64
	mime, etag   string
	checksum     *string
	owner        *uuid.UUID
	metadata     json.RawMessage
	upsert       bool
}

// insertSQL writes u's row; on conflict it replaces the object (upsert) or
// fails with a unique violation.
func insertSQL(upsert bool) string {
	s := `INSERT INTO pgd_storage.objects (bucket, path, version, size, mime_type, etag, checksum, owner, user_metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	if upsert {
		s += ` ON CONFLICT (bucket, path) DO UPDATE SET version = EXCLUDED.version, size = EXCLUDED.size,
		  mime_type = EXCLUDED.mime_type, etag = EXCLUDED.etag, checksum = EXCLUDED.checksum, owner = EXCLUDED.owner,
		  user_metadata = EXCLUDED.user_metadata, updated_at = now()`
	}
	return s + ` RETURNING ` + objectCols
}

func (u upload) args() []any {
	md := u.metadata
	if len(md) == 0 {
		md = json.RawMessage(`{}`)
	}
	return []any{u.bucket, u.path, u.version, u.size, u.mime, u.etag, u.checksum, u.owner, md}
}

// errDryRun rolls a permission check back.
var errDryRun = errors.New("dry run")

// mayWrite checks the caller's policies would let u be written, without
// writing it (before any bytes are stored).
func (e *Edge) mayWrite(ctx context.Context, c *call, req Request, u upload) error {
	u.version, u.etag, u.size = uuid.New(), "pending", max(u.size, 0)
	err := e.asRole(ctx, c, req, func(tx pgx.Tx) error {
		if _, err := scanObject(tx.QueryRow(ctx, insertSQL(u.upsert), u.args()...)); err != nil {
			return err
		}
		return errDryRun
	})
	if errors.Is(err, errDryRun) {
		return nil
	}
	return err
}

// commit writes u's row, as req's role or (signed upload URLs, whose
// signing checked the policies) the edge's login; the stored bytes are
// removed when it fails.
func (e *Edge) commit(ctx context.Context, c *call, req *Request, sc *edgeapi.StorageConfig, cl *storage.Client, u upload) (*Object, error) {
	var o *Object
	var err error
	if req != nil {
		err = e.asRole(ctx, c, *req, func(tx pgx.Tx) error {
			var err error
			o, err = scanObject(tx.QueryRow(ctx, insertSQL(u.upsert), u.args()...))
			return err
		})
	} else {
		var pool interface {
			QueryRow(context.Context, string, ...any) pgx.Row
		}
		if pool, err = e.dbPool(ctx, c.p); err == nil {
			o, err = scanObject(pool.QueryRow(ctx, insertSQL(u.upsert), u.args()...))
		}
	}
	if err != nil {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if derr := cl.Delete(dctx, objectKey(sc, u.version)); derr != nil {
			e.cfg.Log.Warn("remove an upload's bytes", "project", c.p.cfg.Ref, "err", derr)
		}
		cancel()
		return nil, err
	}
	if u.upsert {
		e.collectSoon(c.p)
	}
	return o, nil
}

// checkSize checks a size against the bucket's, the plan's (and the
// route's ceiling) and the quota; size < 0 is not known yet.
func (e *Edge) checkSize(ctx context.Context, c *call, sc *edgeapi.StorageConfig, b *Bucket, size, ceiling int64) error {
	limit := uploadLimit(sc, b, ceiling)
	if size > limit {
		return refuse(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("the object is larger than this bucket allows (%d bytes)", limit))
	}
	if sc.QuotaBytes > 0 && size > 0 {
		pool, err := e.dbPool(ctx, c.p)
		if err != nil {
			return err
		}
		var used int64
		if err := pool.QueryRow(ctx, `SELECT coalesce((SELECT bytes FROM pgd_storage.usage), 0)`).Scan(&used); err != nil {
			return err
		}
		if used+size > sc.QuotaBytes {
			return refuse(http.StatusRequestEntityTooLarge, "quota_exceeded", "the organisation's file storage quota is used up")
		}
	}
	return nil
}

// uploadLimit is the most a single upload of b may hold.
func uploadLimit(sc *edgeapi.StorageConfig, b *Bucket, ceiling int64) int64 {
	limit := min(ceiling, sc.UploadMaxBytes)
	if b.FileSizeLimit != nil {
		limit = min(limit, *b.FileSizeLimit)
	}
	return limit
}

// mimeFor decides an upload's type from what it declares and what its first
// bytes are: a declared image, video or audio type that the bytes contradict
// is refused (MIME spoofing, V4 §13).
func mimeFor(declared string, head []byte) (string, error) {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if t, _, err := mime.ParseMediaType(declared); err == nil {
		declared = t
	} else {
		declared = ""
	}
	sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(head))
	if declared == "" || declared == "application/octet-stream" {
		if len(head) == 0 {
			return "application/octet-stream", nil
		}
		return sniffed, nil
	}
	top, _, _ := strings.Cut(declared, "/")
	stop, _, _ := strings.Cut(sniffed, "/")
	if (top == "image" || top == "video" || top == "audio") && len(head) > 0 && sniffed != "application/octet-stream" && stop != top {
		// SVG sniffs as XML or text.
		if declared != "image/svg+xml" || (sniffed != "text/xml" && sniffed != "text/plain") {
			return "", refuse(http.StatusBadRequest, "mime_mismatch", fmt.Sprintf("the file is declared %s but its contents are %s", declared, sniffed))
		}
	}
	if !mimeTypeRe.MatchString(declared) {
		return "", refuse(http.StatusBadRequest, "invalid_mime_type", "the Content-Type isn't a valid MIME type")
	}
	return declared, nil
}

var mimeTypeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// mimeAllowed checks a type against a bucket's list (empty: any).
func mimeAllowed(b *Bucket, t string) bool {
	if len(b.AllowedMIMETypes) == 0 {
		return true
	}
	top, _, _ := strings.Cut(t, "/")
	for _, a := range b.AllowedMIMETypes {
		if a == t || a == top+"/*" || a == "*/*" {
			return true
		}
	}
	return false
}

// metadataFrom parses user metadata (a JSON object, at most 8 KB).
func metadataFrom(raw string) (json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return json.RawMessage(`{}`), nil
	}
	if len(raw) > 8<<10 {
		return nil, refuse(http.StatusBadRequest, "invalid_metadata", "metadata is at most 8 KB")
	}
	b := []byte(raw)
	if dec, err := base64.StdEncoding.DecodeString(raw); err == nil && json.Valid(dec) {
		b = dec
	}
	if !jsonObject(b) {
		return nil, refuse(http.StatusBadRequest, "invalid_metadata", "metadata must be a JSON object")
	}
	return json.RawMessage(b), nil
}

// body is an upload's bytes: the request body, or the file part of a form.
type body struct {
	r        io.Reader
	size     int64 // -1: not known
	declared string
	metadata string
}

// uploadBody finds the upload's bytes in c's request.
func uploadBody(c *call) (body, error) {
	ct := c.r.Header.Get("Content-Type")
	md := c.r.Header.Get("X-Metadata")
	if t, params, err := mime.ParseMediaType(ct); err == nil && t == "multipart/form-data" {
		mr := multipart.NewReader(c.r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return body{}, refuse(http.StatusBadRequest, "no_file", "the form has no file part")
			}
			if err != nil {
				return body{}, refuse(http.StatusBadRequest, "invalid_body", "the form can't be read: "+err.Error())
			}
			switch {
			case part.FormName() == "metadata":
				b, _ := io.ReadAll(io.LimitReader(part, 8<<10+1))
				md = string(b)
			case part.FileName() != "" || part.FormName() == "file" || part.FormName() == "":
				return body{r: part, size: -1, declared: part.Header.Get("Content-Type"), metadata: md}, nil
			}
		}
	}
	size := c.r.ContentLength
	if size < 0 {
		size = -1
	}
	return body{r: c.r.Body, size: size, declared: ct, metadata: md}, nil
}

// spool writes up to limit bytes of r to a temporary file, so an upload of
// unknown length gets one; it fails past limit.
func spool(r io.Reader, limit int64) (*os.File, int64, error) {
	f, err := os.CreateTemp("", "pgdock-upload-*")
	if err != nil {
		return nil, 0, err
	}
	_ = os.Remove(f.Name()) // gone once closed
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if err != nil {
		_ = f.Close()
		return nil, 0, refuse(http.StatusBadRequest, "incomplete_body", "the upload ended early: "+err.Error())
	}
	if n > limit {
		_ = f.Close()
		return nil, 0, refuse(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("the object is larger than this bucket allows (%d bytes)", limit))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, n, nil
}

// countingHash hashes what passes through and counts it.
type countingHash struct {
	r io.Reader
	h hash.Hash
	n int64
}

func (c *countingHash) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

// putObject stores a direct upload's bytes and writes its row. req nil is a
// signed upload URL (policies already checked); owner is who it's for.
func (e *Edge) putObject(c *call, req *Request, bucket, path string, upsert bool, owner *uuid.UUID) {
	sc, cl, ok := e.store(c)
	if !ok {
		return
	}
	timeout := 10 * time.Minute
	ctx, cancel := context.WithTimeout(c.r.Context(), timeout)
	defer cancel()
	b, err := e.bucket(ctx, c.p, bucket)
	if err != nil {
		e.storageError(c, err)
		return
	}
	src, err := uploadBody(c)
	if err != nil {
		e.storageError(c, err)
		return
	}
	limit := uploadLimit(sc, b, directMax)
	if err := e.checkSize(ctx, c, sc, b, src.size, directMax); err != nil {
		if src.size > directMax && src.size <= sc.UploadMaxBytes {
			c.fail(http.StatusRequestEntityTooLarge, "too_large", "uploads over 50 MB go in parts: start one with POST /storage/v1/upload/{bucket}/{path}")
			return
		}
		e.storageError(c, err)
		return
	}
	md, err := metadataFrom(src.metadata)
	if err != nil {
		e.storageError(c, err)
		return
	}
	br := bufio.NewReaderSize(src.r, 512)
	head, _ := br.Peek(512)
	mt, err := mimeFor(src.declared, head)
	if err != nil {
		e.storageError(c, err)
		return
	}
	if !mimeAllowed(b, mt) {
		c.fail(http.StatusUnsupportedMediaType, "mime_not_allowed", fmt.Sprintf("this bucket doesn't take %s files", mt))
		return
	}
	u := upload{bucket: bucket, path: path, version: uuid.New(), size: src.size, mime: mt, owner: owner, metadata: md, upsert: upsert}
	if req != nil {
		if err := e.mayWrite(ctx, c, *req, u); err != nil {
			e.storageError(c, err)
			return
		}
	}
	var r io.Reader = br
	size := src.size
	if size < 0 {
		f, n, err := spool(br, limit)
		if err != nil {
			e.storageError(c, err)
			return
		}
		defer f.Close()
		if err := e.checkSize(ctx, c, sc, b, n, directMax); err != nil {
			e.storageError(c, err)
			return
		}
		r, size = f, n
	}
	ch := &countingHash{r: io.LimitReader(r, size), h: sha256.New()}
	etag, err := cl.Put(ctx, objectKey(sc, u.version), ch, size, mt)
	if err != nil {
		if ch.n < size {
			c.fail(http.StatusBadRequest, "incomplete_body", "the upload ended before its length")
			return
		}
		e.storageError(c, storeErr(err))
		return
	}
	sum := hex.EncodeToString(ch.h.Sum(nil))
	u.size, u.etag, u.checksum = size, etag, &sum
	o, err := e.commit(ctx, c, req, sc, cl, u)
	if err != nil {
		e.storageError(c, err)
		return
	}
	c.json(http.StatusOK, map[string]any{"id": o.ID, "key": o.Bucket + "/" + o.Path, "bucket": o.Bucket, "path": o.Path,
		"size": o.Size, "mime_type": o.MIMEType, "etag": o.ETag, "checksum": o.Checksum})
}

// ---- Listing -------------------------------------------------------------------

// Entry is a listed name: an object, or a folder (objects below it).
type Entry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Folder bool   `json:"folder"`
	*Object
}

type listInput struct {
	Prefix    string `json:"prefix"`
	Cursor    string `json:"cursor"`
	Limit     int    `json:"limit"`
	Recursive bool   `json:"recursive"`
}

func likePrefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p) + "%"
}

// list answers a bucket's objects the caller may see, a folder at a time
// (or every path under a prefix with recursive).
func (e *Edge) list(c *call, req Request, bucket string, in listInput) {
	if in.Limit <= 0 || in.Limit > 1000 {
		in.Limit = 100
	}
	prefix := strings.TrimLeft(in.Prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") && !in.Recursive {
		prefix += "/"
	}
	if len(prefix) > maxPath || !utf8.ValidString(prefix) {
		c.fail(http.StatusBadRequest, "invalid_prefix", "the prefix isn't a valid path")
		return
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), req.Timeout+3*time.Second)
	defer cancel()
	items := []Entry{}
	next := ""
	err := e.asRole(ctx, c, req, func(tx pgx.Tx) error {
		if in.Recursive {
			rows, err := tx.Query(ctx, `SELECT `+objectCols+` FROM pgd_storage.objects WHERE bucket = $1 AND path LIKE $2 ESCAPE '\'
				AND path > $3 ORDER BY path LIMIT $4`, bucket, likePrefix(prefix), in.Cursor, in.Limit+1)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				o, err := scanObject(rows)
				if err != nil {
					return err
				}
				items = append(items, Entry{Name: pathBase(o.Path), Path: o.Path, Object: o})
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if len(items) > in.Limit {
				items = items[:in.Limit]
				next = items[len(items)-1].Path
			}
			return nil
		}
		curName, curFolder := "", false
		if in.Cursor != "" {
			raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
			var cur struct {
				N string `json:"n"`
				F bool   `json:"f"`
			}
			if err != nil || json.Unmarshal(raw, &cur) != nil {
				return refuse(http.StatusBadRequest, "invalid_cursor", "the cursor isn't one this API gave")
			}
			curName, curFolder = cur.N, cur.F
		}
		rows, err := tx.Query(ctx, `SELECT name, folder,
			  CASE WHEN folder THEN NULL ELSE (array_agg(id))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(version))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(size))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(mime_type))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(etag))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(owner))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(user_metadata))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(created_at))[1] END,
			  CASE WHEN folder THEN NULL ELSE (array_agg(updated_at))[1] END
			FROM (SELECT split_part(substr(path, $3), '/', 1) AS name, strpos(substr(path, $3), '/') > 0 AS folder, o.*
			      FROM pgd_storage.objects o WHERE bucket = $1 AND path LIKE $2 ESCAPE '\') x
			WHERE (name, folder) > ($4, $5)
			GROUP BY name, folder ORDER BY name, folder LIMIT $6`,
			bucket, likePrefix(prefix), utf8.RuneCountInString(prefix)+1, curName, curFolder, in.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var en Entry
			var id, version *uuid.UUID
			var size *int64
			var mt, etag *string
			var owner *uuid.UUID
			var md json.RawMessage
			var created, updated *time.Time
			if err := rows.Scan(&en.Name, &en.Folder, &id, &version, &size, &mt, &etag, &owner, &md, &created, &updated); err != nil {
				return err
			}
			en.Path = prefix + en.Name
			if !en.Folder && id != nil {
				en.Object = &Object{ID: *id, Bucket: bucket, Path: en.Path, Size: *size, MIMEType: *mt, ETag: *etag, Owner: owner,
					UserMetadata: md, CreatedAt: *created, UpdatedAt: *updated}
			}
			items = append(items, en)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > in.Limit {
			items = items[:in.Limit]
			last := items[len(items)-1]
			raw, _ := json.Marshal(map[string]any{"n": last.Name, "f": last.Folder})
			next = base64.RawURLEncoding.EncodeToString(raw)
		}
		return nil
	})
	if err != nil {
		e.storageError(c, err)
		return
	}
	out := map[string]any{"items": items}
	if next != "" {
		out["next_cursor"] = next
	}
	c.json(http.StatusOK, out)
}

// ---- Move, copy, delete --------------------------------------------------------

// moveInput names the source and destination; the Supabase clients' names
// are accepted too.
type moveInput struct {
	Bucket   string `json:"bucket"`
	From     string `json:"from"`
	To       string `json:"to"`
	ToBucket string `json:"to_bucket"`
	Upsert   bool   `json:"upsert"`

	BucketID          string `json:"bucketId"`
	SourceKey         string `json:"sourceKey"`
	DestinationKey    string `json:"destinationKey"`
	DestinationBucket string `json:"destinationBucket"`
}

func (in *moveInput) normalize() *apiErr {
	if in.Bucket == "" {
		in.Bucket = in.BucketID
	}
	if in.From == "" {
		in.From = in.SourceKey
	}
	if in.To == "" {
		in.To = in.DestinationKey
	}
	if in.ToBucket == "" {
		in.ToBucket = in.DestinationBucket
	}
	if in.ToBucket == "" {
		in.ToBucket = in.Bucket
	}
	if !bucketRe.MatchString(in.Bucket) || !bucketRe.MatchString(in.ToBucket) {
		return refuse(http.StatusBadRequest, "invalid_bucket", "bucket and to_bucket must name buckets")
	}
	if !validPath(in.From) || !validPath(in.To) {
		return refuse(http.StatusBadRequest, "invalid_path", "from and to must be object paths")
	}
	return nil
}

// moveOrCopy moves (renames, possibly to another bucket) or copies an
// object, as the caller's policies allow.
func (e *Edge) moveOrCopy(c *call, req Request, copyIt bool) {
	var in moveInput
	if !authBody(c, &in) {
		return
	}
	if a := in.normalize(); a != nil {
		a.send(c)
		return
	}
	sc, cl, ok := e.store(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), 5*time.Minute)
	defer cancel()
	dst, err := e.bucket(ctx, c.p, in.ToBucket)
	if err != nil {
		e.storageError(c, err)
		return
	}
	var src *Object
	if err := e.asRole(ctx, c, req, func(tx pgx.Tx) error {
		var err error
		src, err = getObject(ctx, tx, in.Bucket, in.From)
		return err
	}); err != nil {
		e.storageError(c, err)
		return
	}
	if src == nil {
		c.fail(http.StatusNotFound, "object_not_found", "no such object")
		return
	}
	if dst.FileSizeLimit != nil && src.Size > *dst.FileSizeLimit {
		c.fail(http.StatusRequestEntityTooLarge, "too_large", "the object is larger than the destination bucket allows")
		return
	}
	if !mimeAllowed(dst, src.MIMEType) {
		c.fail(http.StatusUnsupportedMediaType, "mime_not_allowed", fmt.Sprintf("the destination bucket doesn't take %s files", src.MIMEType))
		return
	}
	if !copyIt {
		var o *Object
		err := e.asRole(ctx, c, req, func(tx pgx.Tx) error {
			var err error
			o, err = scanObject(tx.QueryRow(ctx, `UPDATE pgd_storage.objects SET bucket = $3, path = $4, updated_at = now()
				WHERE bucket = $1 AND path = $2 RETURNING `+objectCols, in.Bucket, in.From, in.ToBucket, in.To))
			if errors.Is(err, pgx.ErrNoRows) {
				return refuse(http.StatusForbidden, "not_allowed", "the project's storage policies don't allow moving this object")
			}
			return err
		})
		if err != nil {
			e.storageError(c, err)
			return
		}
		c.json(http.StatusOK, map[string]any{"message": "moved", "object": o})
		return
	}
	if err := e.checkSize(ctx, c, sc, dst, src.Size, sc.UploadMaxBytes); err != nil {
		e.storageError(c, err)
		return
	}
	u := upload{bucket: in.ToBucket, path: in.To, version: uuid.New(), size: src.Size, mime: src.MIMEType, checksum: src.Checksum,
		owner: c.userID, metadata: src.UserMetadata, upsert: in.Upsert}
	if err := e.mayWrite(ctx, c, req, u); err != nil {
		e.storageError(c, err)
		return
	}
	etag, err := cl.Copy(ctx, objectKey(sc, src.version), objectKey(sc, u.version))
	if err != nil {
		e.storageError(c, storeErr(err))
		return
	}
	u.etag = etag
	o, err := e.commit(ctx, c, &req, sc, cl, u)
	if err != nil {
		e.storageError(c, err)
		return
	}
	c.json(http.StatusOK, map[string]any{"message": "copied", "object": o})
}

// remove deletes objects the caller's policies let it delete; their bytes
// go after the change commits.
func (e *Edge) remove(c *call, req Request, bucket string, paths []string, single bool) {
	if len(paths) == 0 || len(paths) > 1000 {
		c.fail(http.StatusBadRequest, "invalid_paths", "name 1–1000 paths to delete")
		return
	}
	for _, p := range paths {
		if !validPath(p) {
			c.fail(http.StatusBadRequest, "invalid_path", "not an object path: "+p)
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), req.Timeout+3*time.Second)
	defer cancel()
	deleted := []*Object{}
	err := e.asRole(ctx, c, req, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM pgd_storage.objects WHERE bucket = $1 AND path = ANY($2) RETURNING `+objectCols, bucket, paths)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanObject(rows)
			if err != nil {
				return err
			}
			deleted = append(deleted, o)
		}
		return rows.Err()
	})
	if err != nil {
		e.storageError(c, err)
		return
	}
	if len(deleted) > 0 {
		e.collectSoon(c.p)
	}
	if single {
		if len(deleted) == 0 {
			c.fail(http.StatusNotFound, "object_not_found", "no such object, or the project's storage policies don't allow deleting it")
			return
		}
		c.json(http.StatusOK, map[string]any{"message": "deleted", "object": deleted[0]})
		return
	}
	c.json(http.StatusOK, deleted)
}

// ---- Clean-up ------------------------------------------------------------------

// collectSoon schedules removing replaced and deleted bytes once the grace
// for downloads in flight has passed.
func (e *Edge) collectSoon(p *project) {
	f := p.files
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.collecting {
		f.mu.Unlock()
		return
	}
	f.collecting = true
	f.mu.Unlock()
	time.AfterFunc(e.cfg.GarbageGrace+time.Second, func() {
		f.mu.Lock()
		f.collecting = false
		f.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if cur := e.lookup(p.cfg.Ref); cur != nil {
			p = cur
		}
		if _, err := e.collect(ctx, p); err != nil {
			e.cfg.Log.Warn("storage clean-up", "project", p.cfg.Ref, "err", err)
		}
	})
}

// collect removes the bytes of versions no row points at any more (older
// than the grace), a batch at a time; it returns how many.
func (e *Edge) collect(ctx context.Context, p *project) (int, error) {
	sc := p.cfg.Storage
	if sc == nil || p.files == nil {
		return 0, nil
	}
	cl, err := p.files.get(sc)
	if err != nil {
		return 0, err
	}
	pool, err := e.dbPool(ctx, p)
	if err != nil {
		return 0, err
	}
	total := 0
	for {
		n := 0
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT version FROM pgd_storage.garbage WHERE at < now() - make_interval(secs => $1)
				ORDER BY at LIMIT 100 FOR UPDATE SKIP LOCKED`, e.cfg.GarbageGrace.Seconds())
			if err != nil {
				return err
			}
			vs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return err
			}
			var done []uuid.UUID
			for _, v := range vs {
				// A row may point at it again (a restore): keep those.
				var used bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pgd_storage.objects WHERE version = $1)`, v).Scan(&used); err != nil {
					return err
				}
				if !used {
					if err := cl.Delete(ctx, objectKey(sc, v)); err != nil {
						return err
					}
					if _, err := cl.DeletePrefix(ctx, sc.Prefix+"transforms/"+v.String()); err != nil {
						return err
					}
				}
				done = append(done, v)
			}
			n = len(done)
			_, err = tx.Exec(ctx, `DELETE FROM pgd_storage.garbage WHERE version = ANY($1)`, done)
			return err
		})
		total += n
		if err != nil || n < 100 {
			return total, err
		}
	}
}
