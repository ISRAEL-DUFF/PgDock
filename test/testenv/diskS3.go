package testenv

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/israel-duff/pgdock/internal/storage"
)

// DiskS3 is a small S3-compatible server that keeps objects and multipart
// parts in files, for uploads too large to hold in memory (the fake S3
// keeps everything in memory). It serves what pgdock's storage client uses:
// objects (ranges, copies), listings and multipart uploads. Signatures are
// not checked.
type DiskS3 struct {
	*httptest.Server
	dir string

	mu      sync.Mutex
	uploads map[string]*diskUpload // by upload id
}

type diskUpload struct {
	key       string
	initiated time.Time
	parts     map[int]string // number -> ETag
}

// NewDiskS3 starts the server on addr ("" for a loopback port), storing
// under a temporary directory removed on Close.
func NewDiskS3(addr string) (*DiskS3, error) {
	dir, err := os.MkdirTemp("", "pgdock-disks3-*")
	if err != nil {
		return nil, err
	}
	d := &DiskS3{dir: dir, uploads: map[string]*diskUpload{}}
	srv := httptest.NewUnstartedServer(d)
	if addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		_ = srv.Listener.Close()
		srv.Listener = ln
	}
	srv.Start()
	d.Server = srv
	return d, nil
}

// Close stops the server and removes its files.
func (d *DiskS3) Close() {
	d.Server.Close()
	_ = os.RemoveAll(d.dir)
}

// Target is a storage target for bucket.
func (d *DiskS3) Target(bucket string) storage.Target {
	return storage.Target{Endpoint: d.URL, Region: "us-east-1", Bucket: bucket, AccessKey: "test", SecretKey: "test", PathStyle: true}
}

func (d *DiskS3) objPath(bucket, key string) string {
	return filepath.Join(d.dir, "objects", url.PathEscape(bucket), url.PathEscape(key))
}

func (d *DiskS3) partPath(id string, n int) string {
	return filepath.Join(d.dir, "parts", id, strconv.Itoa(n))
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

// writeFile streams r into path, returning the size and MD5 (the ETag).
func writeFile(path string, r io.Reader) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, "", err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, "", err
	}
	h := md5.New() //nolint:gosec // S3's ETag
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), os.Rename(tmp, path)
}

func (d *DiskS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket := parts[0]
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	q := r.URL.Query()
	switch {
	case key == "" && r.Method == http.MethodGet && q.Has("uploads"):
		d.listUploads(w, q.Get("prefix"))
	case key == "" && r.Method == http.MethodGet:
		d.list(w, bucket, q.Get("prefix"))
	case r.Method == http.MethodPost && q.Has("uploads"):
		id := randomID()
		d.mu.Lock()
		d.uploads[id] = &diskUpload{key: key, initiated: time.Now().UTC(), parts: map[int]string{}}
		d.mu.Unlock()
		writeXML(w, struct {
			XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
			Bucket   string
			Key      string
			UploadId string //nolint:revive // S3's name
		}{Bucket: bucket, Key: key, UploadId: id})
	case r.Method == http.MethodPut && q.Has("uploadId"):
		d.uploadPart(w, r, q.Get("uploadId"), q.Get("partNumber"))
	case r.Method == http.MethodGet && q.Has("uploadId"):
		d.listParts(w, bucket, key, q.Get("uploadId"))
	case r.Method == http.MethodPost && q.Has("uploadId"):
		d.complete(w, r, bucket, key, q.Get("uploadId"))
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		d.mu.Lock()
		delete(d.uploads, q.Get("uploadId"))
		d.mu.Unlock()
		_ = os.RemoveAll(filepath.Join(d.dir, "parts", q.Get("uploadId")))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		src, _ := url.PathUnescape(r.Header.Get("X-Amz-Copy-Source"))
		sb, sk, _ := strings.Cut(strings.TrimPrefix(src, "/"), "/")
		f, err := os.Open(d.objPath(sb, sk))
		if err != nil {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		defer f.Close()
		_, etag, err := writeFile(d.objPath(bucket, key), f)
		if err != nil {
			s3Error(w, http.StatusInternalServerError, "InternalError")
			return
		}
		writeXML(w, struct {
			XMLName      xml.Name `xml:"CopyObjectResult"`
			ETag         string
			LastModified string
		}{ETag: `"` + etag + `"`, LastModified: time.Now().UTC().Format(time.RFC3339)})
	case r.Method == http.MethodPut:
		_, etag, err := writeFile(d.objPath(bucket, key), r.Body)
		if err != nil {
			s3Error(w, http.StatusInternalServerError, "InternalError")
			return
		}
		w.Header().Set("ETag", `"`+etag+`"`)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		d.get(w, r, bucket, key)
	case r.Method == http.MethodDelete:
		_ = os.Remove(d.objPath(bucket, key))
		w.WriteHeader(http.StatusNoContent)
	default:
		s3Error(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (d *DiskS3) get(w http.ResponseWriter, r *http.Request, bucket, key string) {
	f, err := os.Open(d.objPath(bucket, key))
	if err != nil {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s3Error(w, http.StatusNotFound, "NoSuchKey")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	size := st.Size()
	w.Header().Set("ETag", `"`+strconv.FormatInt(st.ModTime().UnixNano(), 16)+`"`)
	w.Header().Set("Last-Modified", st.ModTime().UTC().Format(http.TimeFormat))
	start, end := int64(0), size-1
	status := http.StatusOK
	if rng := r.Header.Get("Range"); rng != "" {
		spec := strings.TrimPrefix(rng, "bytes=")
		a, b, _ := strings.Cut(spec, "-")
		switch {
		case a == "":
			n, _ := strconv.ParseInt(b, 10, 64)
			start = max(0, size-n)
		default:
			start, _ = strconv.ParseInt(a, 10, 64)
			if b != "" {
				end, _ = strconv.ParseInt(b, 10, 64)
			}
		}
		if start >= size {
			s3Error(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
			return
		}
		end = min(end, size-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		status = http.StatusPartialContent
	}
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = f.Seek(start, io.SeekStart)
	_, _ = io.CopyN(w, f, end-start+1)
}

func (d *DiskS3) list(w http.ResponseWriter, bucket, prefix string) {
	type content struct {
		Key          string
		Size         int64
		LastModified string
	}
	var out []content
	entries, _ := os.ReadDir(filepath.Join(d.dir, "objects", url.PathEscape(bucket)))
	for _, e := range entries {
		k, err := url.PathUnescape(e.Name())
		if err != nil || strings.HasSuffix(k, ".tmp") || !strings.HasPrefix(k, prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, content{Key: k, Size: info.Size(), LastModified: info.ModTime().UTC().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeXML(w, struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		Name        string
		Prefix      string
		KeyCount    int
		IsTruncated bool
		Contents    []content
	}{Name: bucket, Prefix: prefix, KeyCount: len(out), Contents: out})
}

func (d *DiskS3) listUploads(w http.ResponseWriter, prefix string) {
	type upload struct {
		Key       string
		UploadId  string //nolint:revive // S3's name
		Initiated string
	}
	var out []upload
	d.mu.Lock()
	for id, u := range d.uploads {
		if strings.HasPrefix(u.key, prefix) {
			out = append(out, upload{Key: u.key, UploadId: id, Initiated: u.initiated.Format(time.RFC3339)})
		}
	}
	d.mu.Unlock()
	writeXML(w, struct {
		XMLName     xml.Name `xml:"ListMultipartUploadsResult"`
		IsTruncated bool
		Upload      []upload
	}{Upload: out})
}

func (d *DiskS3) uploadPart(w http.ResponseWriter, r *http.Request, id, number string) {
	n, err := strconv.Atoi(number)
	d.mu.Lock()
	u := d.uploads[id]
	d.mu.Unlock()
	if u == nil || err != nil || n < 1 || n > 10000 {
		s3Error(w, http.StatusNotFound, "NoSuchUpload")
		return
	}
	_, etag, err := writeFile(d.partPath(id, n), r.Body)
	if err != nil {
		s3Error(w, http.StatusBadRequest, "IncompleteBody")
		return
	}
	d.mu.Lock()
	u.parts[n] = etag
	d.mu.Unlock()
	w.Header().Set("ETag", `"`+etag+`"`)
}

func (d *DiskS3) listParts(w http.ResponseWriter, bucket, key, id string) {
	type part struct {
		PartNumber   int
		ETag         string
		Size         int64
		LastModified string
	}
	d.mu.Lock()
	u := d.uploads[id]
	var out []part
	if u != nil {
		for n, etag := range u.parts {
			st, err := os.Stat(d.partPath(id, n))
			if err != nil {
				continue
			}
			out = append(out, part{PartNumber: n, ETag: `"` + etag + `"`, Size: st.Size(), LastModified: st.ModTime().UTC().Format(time.RFC3339)})
		}
	}
	d.mu.Unlock()
	if u == nil {
		s3Error(w, http.StatusNotFound, "NoSuchUpload")
		return
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PartNumber < out[j].PartNumber })
	writeXML(w, struct {
		XMLName     xml.Name `xml:"ListPartsResult"`
		Bucket      string
		Key         string
		UploadId    string //nolint:revive // S3's name
		IsTruncated bool
		Part        []part
	}{Bucket: bucket, Key: key, UploadId: id, Part: out})
}

func (d *DiskS3) complete(w http.ResponseWriter, r *http.Request, bucket, key, id string) {
	var in struct {
		Part []struct {
			PartNumber int
		}
	}
	if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
		s3Error(w, http.StatusBadRequest, "MalformedXML")
		return
	}
	d.mu.Lock()
	u := d.uploads[id]
	delete(d.uploads, id)
	d.mu.Unlock()
	if u == nil {
		s3Error(w, http.StatusNotFound, "NoSuchUpload")
		return
	}
	pr, pw := io.Pipe()
	go func() {
		for _, p := range in.Part {
			f, err := os.Open(d.partPath(id, p.PartNumber))
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			_, err = io.Copy(pw, f)
			_ = f.Close()
			_ = os.Remove(d.partPath(id, p.PartNumber))
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
		_ = pw.Close()
	}()
	_, etag, err := writeFile(d.objPath(bucket, key), pr)
	_ = os.RemoveAll(filepath.Join(d.dir, "parts", id))
	if err != nil {
		s3Error(w, http.StatusBadRequest, "InvalidPart")
		return
	}
	writeXML(w, struct {
		XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
		Bucket  string
		Key     string
		ETag    string
	}{Bucket: bucket, Key: key, ETag: `"` + etag + `-` + strconv.Itoa(len(in.Part)) + `"`})
}
