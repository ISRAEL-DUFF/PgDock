package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gen2brain/webp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/edge"
	"github.com/israel-duff/pgdock/internal/services"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// files calls a project's /storage/v1 with raw bodies.
type files struct {
	t   *testing.T
	ed  *testenv.Edge
	ref string
	key string
}

type fileResult struct {
	Code   int
	Header http.Header
	Body   []byte
	Error  struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (r fileResult) String() string { return fmt.Sprintf("%d %s", r.Code, r.Body) }

func (f files) do(method, path string, body io.Reader, token string, headers ...string) fileResult {
	f.t.Helper()
	req, err := http.NewRequest(method, f.ed.URL+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Host = f.ref + "." + testenv.EdgeDomain
	if f.key != "" {
		req.Header.Set("apikey", f.key)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	r := fileResult{Code: res.StatusCode, Header: res.Header, Body: b}
	_ = json.Unmarshal(b, &r)
	return r
}

func (f files) json(method, path, body, token string) fileResult {
	f.t.Helper()
	return f.do(method, path, strings.NewReader(body), token, "Content-Type", "application/json")
}

// pngOf is a w×h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// storageProject is a project with backend services and file storage, its
// keys, and two users' tokens.
type storageProject struct {
	e           *testenv.Env
	ed          *testenv.Edge
	ref         string
	pid         uuid.UUID
	db          string
	instance    uuid.UUID
	anon, admin files
	alice, bob  string
	aliceID     uuid.UUID
	bobID       uuid.UUID
}

func newStorageProject(t *testing.T, name string, onDisk bool) *storageProject {
	e := testenv.Start(t, testenv.Options{})
	if onDisk {
		// Too large for the in-memory fake.
		d, err := testenv.NewDiskS3("")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		e.SaveStorage(d.Target("pgdock-test"))
	} else {
		e.ConfigureBackups()
	}
	ctx := context.Background()
	creds := e.CreateProject(name)
	pid := creds.Project.Id
	p, err := store.New(e.DB).GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	var en gen.BackendServicesEnabled
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services", nil, &en); code != http.StatusAccepted {
		t.Fatalf("enable: %d", code)
	}
	if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var pub, sec string
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			pub = k.Value
		} else {
			sec = k.Value
		}
	}
	ref := *en.Services.Ref
	ed := e.StartEdge(func(c *edge.Config) { c.GarbageGrace = time.Millisecond })
	sp := &storageProject{e: e, ed: ed, ref: ref, pid: pid, db: p.DbName, instance: p.InstanceID,
		anon: files{t: t, ed: ed, ref: ref, key: pub}, admin: files{t: t, ed: ed, ref: ref, key: sec}}
	waitFor(t, 30*time.Second, "the edge reaches the project's database", func() bool {
		return sp.anon.do("GET", "/data/v1/health", nil, "").Code == 200
	})
	api := authAPI{t: t, ed: ed, ref: ref, key: sec}
	user := func(email string) (string, uuid.UUID) {
		if r := api.post("/auth/v1/admin/users", fmt.Sprintf(`{"email":%q,"password":"password-123456","email_confirm":true}`, email)); r.Code != 201 {
			t.Fatalf("create %s: %d %s", email, r.Code, r.Body)
		}
		r := authAPI{t: t, ed: ed, ref: ref, key: pub}.post("/auth/v1/signin/password", fmt.Sprintf(`{"email":%q,"password":"password-123456"}`, email))
		if r.Code != 200 {
			t.Fatalf("sign in %s: %d %s", email, r.Code, r.Body)
		}
		return r.Session.AccessToken, r.Session.User.ID
	}
	sp.alice, sp.aliceID = user("alice@example.com")
	sp.bob, sp.bobID = user("bob@example.com")
	// The owner's policy (V4 §5.1): users read and write their own avatars.
	app := e.MustConnect(creds.Connection.PooledUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, fmt.Sprintf(`CREATE POLICY own_avatar ON pgd_storage.objects FOR ALL TO %q
		USING (bucket = 'avatars' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text)
		WITH CHECK (bucket = 'avatars' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text)`, store.UserRole(p.DbName))); err != nil {
		t.Fatalf("policy: %v", err)
	}
	return sp
}

// TestStorage covers V4-M33's done-when (V4 §5) except the 2 GB upload
// (TestStorageLargeUpload): per-user avatar policies hold for upload, read
// and delete, and a transformed image is served from cache the second
// time. Around it: buckets, MIME and path checks, ranges, listing, signed
// URLs, public buckets, moves and copies, and the bytes' clean-up.
func TestStorage(t *testing.T) {
	sp := newStorageProject(t, "files-app", false)
	anon, admin := sp.anon, sp.admin
	alice, bob := sp.alice, sp.bob

	// ---- Buckets: the secret key only -----------------------------------------
	if r := anon.json("POST", "/storage/v1/bucket", `{"id":"avatars"}`, alice); r.Code != 403 || r.Error.Code != "secret_key_required" {
		t.Fatalf("a bucket with the publishable key: %s", r)
	}
	for _, bad := range []string{"Avatars", "public", "a/b", uuid.NewString()} {
		if r := admin.json("POST", "/storage/v1/bucket", fmt.Sprintf(`{"id":%q}`, bad), ""); r.Code != 400 {
			t.Fatalf("bucket %q: %s", bad, r)
		}
	}
	if r := admin.json("POST", "/storage/v1/bucket", `{"id":"avatars","allowed_mime_types":["image/*"],"file_size_limit":1048576}`, ""); r.Code != 201 {
		t.Fatalf("create avatars: %s", r)
	}
	if r := admin.json("POST", "/storage/v1/bucket", `{"id":"avatars"}`, ""); r.Code != 409 {
		t.Fatalf("a second avatars: %s", r)
	}
	if r := admin.do("GET", "/storage/v1/bucket", nil, ""); r.Code != 200 || !strings.Contains(string(r.Body), `"id":"avatars"`) {
		t.Fatalf("list buckets: %s", r)
	}

	// ---- Per-user avatars: upload ---------------------------------------------
	me := pngOf(t, 400, 300)
	alicePath := "/storage/v1/object/avatars/" + sp.aliceID.String() + "/me.png"
	if r := anon.do("POST", alicePath, bytes.NewReader(me), alice, "Content-Type", "image/png"); r.Code != 200 {
		t.Fatalf("alice uploads her avatar: %s", r)
	}
	if r := anon.do("POST", "/storage/v1/object/avatars/"+sp.bobID.String()+"/x.png", bytes.NewReader(me), alice, "Content-Type", "image/png"); r.Code != 403 ||
		r.Error.Code != "not_allowed" {
		t.Fatalf("alice uploads into bob's folder: %s", r)
	}
	if r := anon.do("POST", "/storage/v1/object/avatars/"+sp.aliceID.String()+"/anon.png", bytes.NewReader(me), "", "Content-Type", "image/png"); r.Code != 403 {
		t.Fatalf("anon uploads: %s", r)
	}
	if r := anon.do("POST", alicePath, bytes.NewReader(me), alice, "Content-Type", "image/png"); r.Code != 409 {
		t.Fatalf("an upload over an existing object without upsert: %s", r)
	}
	// MIME and size checks.
	if r := anon.do("POST", "/storage/v1/object/avatars/"+sp.aliceID.String()+"/fake.png", strings.NewReader("<html><script>alert(1)</script></html>"), alice,
		"Content-Type", "image/png"); r.Code != 400 || r.Error.Code != "mime_mismatch" {
		t.Fatalf("HTML posing as PNG: %s", r)
	}
	if r := anon.do("POST", "/storage/v1/object/avatars/"+sp.aliceID.String()+"/notes.txt", strings.NewReader("hello"), alice,
		"Content-Type", "text/plain"); r.Code != 415 || r.Error.Code != "mime_not_allowed" {
		t.Fatalf("text in an image bucket: %s", r)
	}
	big := make([]byte, 1<<20+1)
	copy(big, me)
	if r := anon.do("POST", "/storage/v1/object/avatars/"+sp.aliceID.String()+"/big.png", bytes.NewReader(big), alice,
		"Content-Type", "image/png"); r.Code != 413 || r.Error.Code != "too_large" {
		t.Fatalf("over the bucket's size limit: %s", r)
	}
	for _, bad := range []string{"/storage/v1/object/avatars/" + sp.aliceID.String() + "/%2e%2e/x.png", "/storage/v1/object/avatars//x.png",
		"/storage/v1/object/avatars/" + sp.aliceID.String() + "/a%00b.png"} {
		if r := anon.do("POST", bad, bytes.NewReader(me), alice, "Content-Type", "image/png"); r.Code != 400 || r.Error.Code != "invalid_path" {
			t.Fatalf("path %s: %s", bad, r)
		}
	}

	// ---- … read ------------------------------------------------------------------
	r := anon.do("GET", alicePath, nil, alice)
	if r.Code != 200 || !bytes.Equal(r.Body, me) || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("ETag") == "" ||
		r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("alice reads her avatar: %d %v", r.Code, r.Header)
	}
	etag := r.Header.Get("ETag")
	if r := anon.do("GET", alicePath, nil, alice, "If-None-Match", etag); r.Code != 304 {
		t.Fatalf("a conditional read: %d", r.Code)
	}
	if r := anon.do("GET", alicePath, nil, alice, "Range", "bytes=10-19"); r.Code != 206 || !bytes.Equal(r.Body, me[10:20]) ||
		r.Header.Get("Content-Range") != fmt.Sprintf("bytes 10-19/%d", len(me)) {
		t.Fatalf("a range: %d %v %d", r.Code, r.Header, len(r.Body))
	}
	if r := anon.do("GET", alicePath, nil, bob); r.Code != 404 {
		t.Fatalf("bob reads alice's avatar: %s", r)
	}
	if r := anon.do("GET", alicePath, nil, ""); r.Code != 404 {
		t.Fatalf("anon reads alice's avatar: %s", r)
	}
	if r := admin.do("GET", alicePath, nil, ""); r.Code != 200 || !bytes.Equal(r.Body, me) {
		t.Fatalf("the service role reads it: %d", r.Code)
	}
	if r := anon.do("GET", "/storage/v1/object/info/avatars/"+sp.aliceID.String()+"/me.png", nil, alice); r.Code != 200 ||
		!strings.Contains(string(r.Body), `"mime_type":"image/png"`) || !strings.Contains(string(r.Body), sp.aliceID.String()) {
		t.Fatalf("info: %s", r)
	}
	// Listing shows only what the policies let each see.
	for _, tc := range []struct {
		token string
		n     int
	}{{alice, 1}, {bob, 0}} {
		r := anon.do("GET", "/storage/v1/list/avatars?prefix="+sp.aliceID.String(), nil, tc.token)
		var out struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(r.Body, &out); r.Code != 200 || len(out.Items) != tc.n {
			t.Fatalf("list: %s", r)
		}
	}
	r = anon.do("GET", "/storage/v1/list/avatars", nil, alice)
	if r.Code != 200 || !strings.Contains(string(r.Body), `"name":"`+sp.aliceID.String()+`","path":"`+sp.aliceID.String()+`","folder":true`) {
		t.Fatalf("the top level is alice's folder: %s", r)
	}

	// ---- Signed URLs ---------------------------------------------------------------
	r = anon.json("POST", "/storage/v1/object/sign/avatars/"+sp.aliceID.String()+"/me.png", `{"expires_in":60}`, alice)
	var signed struct {
		SignedURL string `json:"signed_url"`
	}
	if json.Unmarshal(r.Body, &signed); r.Code != 200 || signed.SignedURL == "" {
		t.Fatalf("sign: %s", r)
	}
	if r := anon.json("POST", "/storage/v1/object/sign/avatars/"+sp.aliceID.String()+"/me.png", `{}`, bob); r.Code != 404 {
		t.Fatalf("bob signs alice's avatar: %s", r)
	}
	su, _ := url.Parse(signed.SignedURL)
	keyless := files{t: t, ed: sp.ed, ref: sp.ref}
	if r := keyless.do("GET", su.RequestURI(), nil, ""); r.Code != 200 || !bytes.Equal(r.Body, me) {
		t.Fatalf("a signed URL without a key: %s", r)
	}
	tampered := strings.Replace(su.RequestURI(), "me.png", "other.png", 1)
	if r := keyless.do("GET", tampered, nil, ""); r.Code != 400 || r.Error.Code != "invalid_signature" {
		t.Fatalf("a signed URL for another path: %s", r)
	}
	q := su.Query()
	tok := q.Get("token")
	q.Set("token", tok[:len(tok)-2]+"xx")
	su.RawQuery = q.Encode()
	if r := keyless.do("GET", su.RequestURI(), nil, ""); r.Code != 400 {
		t.Fatalf("a tampered signature: %s", r)
	}
	r = anon.json("POST", "/storage/v1/object/sign/avatars/"+sp.aliceID.String()+"/me.png", `{"expires_in":1}`, alice)
	_ = json.Unmarshal(r.Body, &signed)
	time.Sleep(1100 * time.Millisecond)
	su, _ = url.Parse(signed.SignedURL)
	if r := keyless.do("GET", su.RequestURI(), nil, ""); r.Code != 400 {
		t.Fatalf("an expired signed URL: %s", r)
	}
	// A signed upload URL: the policy is checked when it's made.
	if r := anon.json("POST", "/storage/v1/sign-upload/avatars/"+sp.bobID.String()+"/x.png", `{}`, alice); r.Code != 403 {
		t.Fatalf("alice signs an upload into bob's folder: %s", r)
	}
	r = anon.json("POST", "/storage/v1/sign-upload/avatars/"+sp.aliceID.String()+"/via-url.png", `{}`, alice)
	var up struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(r.Body, &up); r.Code != 200 || up.URL == "" {
		t.Fatalf("sign an upload: %s", r)
	}
	uu, _ := url.Parse(up.URL)
	if r := keyless.do("PUT", uu.RequestURI(), bytes.NewReader(me), "", "Content-Type", "image/png"); r.Code != 200 {
		t.Fatalf("upload to a signed URL: %s", r)
	}
	if r := anon.do("GET", "/storage/v1/object/avatars/"+sp.aliceID.String()+"/via-url.png", nil, alice); r.Code != 200 || !bytes.Equal(r.Body, me) {
		t.Fatalf("read the signed upload: %d", r.Code)
	}

	// ---- Public buckets ----------------------------------------------------------
	if r := admin.json("POST", "/storage/v1/bucket", `{"id":"site","public":true,"cache_seconds":600}`, ""); r.Code != 201 {
		t.Fatalf("create site: %s", r)
	}
	logo := pngOf(t, 32, 32)
	if r := admin.do("POST", "/storage/v1/object/site/img/logo.png", bytes.NewReader(logo), "", "Content-Type", "image/png"); r.Code != 200 {
		t.Fatalf("upload the logo: %s", r)
	}
	r = keyless.do("GET", "/storage/v1/public/site/img/logo.png", nil, "")
	if r.Code != 200 || !bytes.Equal(r.Body, logo) || r.Header.Get("Cache-Control") != "public, max-age=600" {
		t.Fatalf("a public file: %d %v", r.Code, r.Header)
	}
	if r := keyless.do("GET", "/storage/v1/public/avatars/"+sp.aliceID.String()+"/me.png", nil, ""); r.Code != 404 {
		t.Fatalf("a private file by the public URL: %s", r)
	}
	if r := admin.json("PUT", "/storage/v1/bucket/site", `{"public":false}`, ""); r.Code != 200 {
		t.Fatalf("make site private: %s", r)
	}
	if r := keyless.do("GET", "/storage/v1/public/site/img/logo.png", nil, ""); r.Code != 404 {
		t.Fatalf("a bucket made private: %s", r)
	}

	// ---- Image transforms: rendered once, then from the cache -----------------------
	render := "/storage/v1/render/avatars/" + sp.aliceID.String() + "/me.png?width=100&format=webp"
	r = anon.do("GET", render, nil, alice)
	if r.Code != 200 || r.Header.Get("X-Cache") != "MISS" || r.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("render: %d %v %s", r.Code, r.Header, r.Body)
	}
	img, err := webp.Decode(bytes.NewReader(r.Body))
	if err != nil || img.Bounds().Dx() != 100 || img.Bounds().Dy() != 75 {
		t.Fatalf("rendered image: %v %v", err, img)
	}
	first := r.Body
	r = anon.do("GET", render, nil, alice)
	if r.Code != 200 || r.Header.Get("X-Cache") != "HIT" || !bytes.Equal(r.Body, first) {
		t.Fatalf("render again: %d %v", r.Code, r.Header)
	}
	if r := anon.do("GET", render, nil, bob); r.Code != 404 {
		t.Fatalf("bob renders alice's avatar: %s", r)
	}
	r = anon.do("GET", "/storage/v1/render/avatars/"+sp.aliceID.String()+"/me.png?width=50&height=50&resize=cover&format=jpeg&quality=70", nil, alice)
	if r.Code != 200 || r.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("a cover crop: %s", r)
	}

	// ---- Move and copy --------------------------------------------------------------
	r = anon.json("POST", "/storage/v1/object/copy", fmt.Sprintf(`{"bucket":"avatars","from":"%s/me.png","to":"%s/copy.png"}`, sp.aliceID, sp.aliceID), alice)
	if r.Code != 200 {
		t.Fatalf("copy: %s", r)
	}
	if r := anon.json("POST", "/storage/v1/object/copy", fmt.Sprintf(`{"bucket":"avatars","from":"%s/me.png","to":"%s/mine.png"}`, sp.aliceID, sp.bobID), alice); r.Code != 403 {
		t.Fatalf("copy into bob's folder: %s", r)
	}
	r = anon.json("POST", "/storage/v1/object/move", fmt.Sprintf(`{"bucket":"avatars","from":"%s/copy.png","to":"%s/moved.png"}`, sp.aliceID, sp.aliceID), alice)
	if r.Code != 200 {
		t.Fatalf("move: %s", r)
	}
	if r := anon.do("GET", "/storage/v1/object/avatars/"+sp.aliceID.String()+"/moved.png", nil, alice); r.Code != 200 || !bytes.Equal(r.Body, me) {
		t.Fatalf("read the moved copy: %d", r.Code)
	}
	if r := anon.json("POST", "/storage/v1/object/move", fmt.Sprintf(`{"bucket":"avatars","from":"%s/moved.png","to":"%s/stolen.png"}`, sp.aliceID, sp.aliceID), bob); r.Code != 404 {
		t.Fatalf("bob moves alice's file: %s", r)
	}

	// ---- … delete, and the bytes go too ---------------------------------------------
	versions := objectKeys(t, sp)
	if r := anon.do("DELETE", alicePath, nil, bob); r.Code != 404 {
		t.Fatalf("bob deletes alice's avatar: %s", r)
	}
	if r := anon.do("DELETE", alicePath, nil, alice); r.Code != 200 {
		t.Fatalf("alice deletes her avatar: %s", r)
	}
	if r := anon.do("GET", alicePath, nil, alice); r.Code != 404 {
		t.Fatalf("read after delete: %s", r)
	}
	waitFor(t, 20*time.Second, "the deleted avatar's bytes and renders are removed", func() bool {
		return len(objectKeys(t, sp)) < len(versions)
	})
	r = anon.json("DELETE", "/storage/v1/object/avatars", fmt.Sprintf(`{"paths":["%s/moved.png","%s/via-url.png","%s/none.png"]}`, sp.aliceID, sp.aliceID, sp.aliceID), alice)
	var gone []json.RawMessage
	if json.Unmarshal(r.Body, &gone); r.Code != 200 || len(gone) != 2 {
		t.Fatalf("bulk delete: %s", r)
	}
	if r := admin.do("DELETE", "/storage/v1/bucket/site", nil, ""); r.Code != 409 || r.Error.Code != "bucket_not_empty" {
		t.Fatalf("delete a bucket with files: %s", r)
	}
	if r := admin.do("POST", "/storage/v1/bucket/site/empty", nil, ""); r.Code != 200 {
		t.Fatalf("empty: %s", r)
	}
	if r := admin.do("DELETE", "/storage/v1/bucket/site", nil, ""); r.Code != 200 {
		t.Fatalf("delete the empty bucket: %s", r)
	}
	waitFor(t, 20*time.Second, "every deleted object's bytes are removed", func() bool {
		return len(objectKeys(t, sp)) == 0
	})

	// Turning row-level security off stops the publishable key.
	ctx := context.Background()
	admConn := sp.adminConn(t)
	defer admConn.Close(ctx)
	if _, err := admConn.Exec(ctx, `ALTER TABLE pgd_storage.objects DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if r := anon.do("GET", "/storage/v1/list/avatars", nil, alice); r.Code != 403 || r.Error.Code != "rls_required" {
		t.Fatalf("no row-level security: %s", r)
	}
	if _, err := admConn.Exec(ctx, `ALTER TABLE pgd_storage.objects ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
}

// adminConn is the platform's admin connection to the project database.
func (sp *storageProject) adminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := sp.e.Service.AdminConn(context.Background(), sp.instance, sp.db)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// objectKeys are the project's object versions in the fake store.
func objectKeys(t *testing.T, sp *storageProject) []string {
	t.Helper()
	keys, err := sp.e.S3.Objects("pgdock-test")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range keys {
		if strings.Contains(k, services.FilesPrefix(sp.ref)) {
			out = append(out, k)
		}
	}
	return out
}

// TestStorageLargeUpload is the rest of V4-M33's done-when: a 2 GB file
// (PGDOCK_TEST_LARGE_UPLOAD_MB to change it) uploads in parts straight to
// the object store with presigned URLs, one part retried, and reads back
// whole.
func TestStorageLargeUpload(t *testing.T) {
	mb := 2048
	if v, err := strconv.Atoi(os.Getenv("PGDOCK_TEST_LARGE_UPLOAD_MB")); err == nil && v > 0 {
		mb = v
	}
	sp := newStorageProject(t, "big-files", true)
	if r := sp.admin.json("POST", "/storage/v1/bucket", `{"id":"videos"}`, ""); r.Code != 201 {
		t.Fatalf("create videos: %s", r)
	}
	// Videos: users upload into their own folder.
	ctx := context.Background()
	adm := sp.adminConn(t)
	defer adm.Close(ctx)
	if _, err := adm.Exec(ctx, fmt.Sprintf(`CREATE POLICY own_videos ON pgd_storage.objects FOR ALL TO %q
		USING (bucket = 'videos' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text)
		WITH CHECK (bucket = 'videos' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text)`, store.UserRole(sp.db))); err != nil {
		t.Fatal(err)
	}
	size := int64(mb) << 20
	path := sp.aliceID.String() + "/talk.mp4"
	if r := sp.anon.json("POST", "/storage/v1/upload/videos/"+path, fmt.Sprintf(`{"size":%d,"mime_type":"video/mp4"}`, size), sp.bob); r.Code != 403 {
		t.Fatalf("bob starts an upload into alice's folder: %s", r)
	}
	if r := sp.anon.do("POST", "/storage/v1/object/videos/"+path, io.LimitReader(rand.Reader, 60<<20), sp.alice,
		"Content-Type", "video/mp4", "Content-Length", strconv.Itoa(60<<20)); r.Code != 413 {
		t.Fatalf("a direct upload over 50 MB: %s", r)
	}
	r := sp.anon.json("POST", "/storage/v1/upload/videos/"+path, fmt.Sprintf(`{"size":%d,"mime_type":"video/mp4"}`, size), sp.alice)
	var start struct {
		ID       uuid.UUID `json:"id"`
		PartSize int64     `json:"part_size"`
		Parts    []struct {
			Number int    `json:"number"`
			Size   int64  `json:"size"`
			URL    string `json:"url"`
		} `json:"parts"`
	}
	if json.Unmarshal(r.Body, &start); r.Code != 200 || len(start.Parts) == 0 {
		t.Fatalf("start: %s", r)
	}
	// The bytes: a deterministic stream, hashed as it goes.
	seed := sha256.Sum256([]byte("pgdock large upload"))
	gen := func(part int, n int64) io.Reader { return &prng{seed: seed, part: part, left: n} }
	whole := sha256.New()
	put := func(u string, body io.Reader, n int64) int {
		req, err := http.NewRequest(http.MethodPut, u, body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = n
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return res.StatusCode
	}
	began := time.Now()
	for i, p := range start.Parts {
		if i == 1 {
			// A part that fails midway is sent again.
			_ = put(p.URL, io.LimitReader(gen(p.Number, p.Size), p.Size/2), p.Size/2)
		}
		if code := put(p.URL, io.TeeReader(gen(p.Number, p.Size), whole), p.Size); code != 200 {
			t.Fatalf("part %d: %d", p.Number, code)
		}
	}
	t.Logf("%d MB in %d parts uploaded in %s", mb, len(start.Parts), time.Since(began).Round(time.Second))
	if r := sp.anon.json("POST", fmt.Sprintf("/storage/v1/upload/%s/complete", start.ID), `{}`, ""); r.Code != 200 {
		t.Fatalf("complete: %s", r)
	}
	// It reads back whole.
	req, _ := http.NewRequest("GET", sp.ed.URL+"/storage/v1/object/videos/"+path, nil)
	req.Host = sp.ref + "." + testenv.EdgeDomain
	req.Header.Set("apikey", sp.anon.key)
	req.Header.Set("Authorization", "Bearer "+sp.alice)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	back := sha256.New()
	n, _ := io.Copy(back, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || n != size || hex.EncodeToString(back.Sum(nil)) != hex.EncodeToString(whole.Sum(nil)) {
		t.Fatalf("read back: %d, %d of %d bytes, same: %v", res.StatusCode, n, size, bytes.Equal(back.Sum(nil), whole.Sum(nil)))
	}
	if r := sp.anon.json("POST", fmt.Sprintf("/storage/v1/upload/%s/complete", start.ID), `{}`, ""); r.Code != 404 {
		t.Fatalf("complete twice: %s", r)
	}
}

// prng is a deterministic byte stream per part.
type prng struct {
	seed  [32]byte
	part  int
	block int
	buf   []byte
	left  int64
}

func (p *prng) Read(b []byte) (int, error) {
	if p.left <= 0 {
		return 0, io.EOF
	}
	n := 0
	for n < len(b) && p.left > 0 {
		if len(p.buf) == 0 {
			h := sha256.New()
			h.Write(p.seed[:])
			fmt.Fprintf(h, "%d/%d", p.part, p.block)
			p.block++
			sum := h.Sum(nil)
			p.buf = bytes.Repeat(sum, 128) // 4 KB
		}
		k := copy(b[n:], p.buf)
		if int64(k) > p.left {
			k = int(p.left)
		}
		p.buf = p.buf[k:]
		n += k
		p.left -= int64(k)
	}
	return n, nil
}
