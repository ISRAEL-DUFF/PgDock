package pgdock

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEncodeCondition(t *testing.T) {
	for _, c := range []struct {
		col  string
		op   Op
		v    any
		want string
	}{
		{"done", OpEq, false, "done:eq:false"},
		{"id", OpIn, []any{1, "a,b", `q"x`}, `id:in:1,"a,b","q\"x"`},
		{"deleted_at", OpIs, nil, "deleted_at:is:null"},
		{"at", OpGte, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "at:gte:2026-01-02T03:04:05Z"},
		{"tags", OpContains, []string{"a"}, `tags:contains:["a"]`},
	} {
		if got := EncodeCondition(c.col, c.op, c.v); got != c.want {
			t.Errorf("%s %s %v: got %s want %s", c.col, c.op, c.v, got, c.want)
		}
	}
}

type fake struct {
	mu    sync.Mutex
	calls []*http.Request
	body  map[*http.Request]string
	h     func(w http.ResponseWriter, r *http.Request, body string)
}

func newFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body string)) (*fake, *Client) {
	f := &fake{h: h, body: map[*http.Request]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r)
		f.body[r] = string(b)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		f.h(w, r, string(b))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "pgd_pub_x")
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestQueries(t *testing.T) {
	f, c := newFake(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == http.MethodPatch {
			_, _ = io.WriteString(w, `{"affected":1,"data":[{"id":7}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":1,"title":"a"}],"next_cursor":"c2","count":5}`)
	})
	ctx := context.Background()
	type todo struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	rows, page, err := List[todo](ctx, c.Data.From("todos").Select("id,title").Eq("done", false).
		Or(Cond("a", OpEq, 1), Cond("b", OpEq, 2)).Order("created_at", true).Limit(20).Count("exact"))
	if err != nil || len(rows) != 1 || rows[0].Title != "a" || page.NextCursor != "c2" || page.Count == nil || *page.Count != 5 {
		t.Fatalf("list: %v %+v %+v", err, rows, page)
	}
	r := f.calls[0]
	if r.Method != "GET" || r.URL.Path != "/data/v1/todos" || r.URL.Query().Get("where") != "done:eq:false" ||
		r.URL.Query().Get("or") != "a:eq:1,b:eq:2" || r.URL.Query().Get("order") != "created_at:desc" || r.Header.Get("apikey") != "pgd_pub_x" {
		t.Fatalf("read: %s %s", r.Method, r.URL)
	}
	// not() needs the JSON form.
	if _, err := c.Data.From("api.items").Select("").Not(Cond("x", OpIs, nil)).Replica().Into(ctx, nil); err != nil {
		t.Fatal(err)
	}
	q := f.calls[1]
	var body map[string]any
	_ = json.Unmarshal([]byte(f.body[q]), &body)
	where, _ := json.Marshal(body["where"])
	if q.Method != "POST" || q.URL.Path != "/data/v1/api.items/query" || string(where) != `{"and":[{"not":{"column":"x","op":"is","value":null}}]}` ||
		q.Header.Get("Read-Replica") != "allowed" {
		t.Fatalf("json query: %s %s %s", q.Method, q.URL, where)
	}
	var out []todo
	n, err := c.Data.From("todos").Update(map[string]any{"done": true}).Eq("id", 7).AtMost(1).Exec(ctx, &out)
	if err != nil || n != 1 || len(out) != 1 || f.calls[2].URL.Query().Get("max_affected") != "1" {
		t.Fatalf("update: %d %v %v", n, out, err)
	}
}

func TestErrors(t *testing.T) {
	_, c := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":{"code":"rls_required","message":"enable it","request_id":"req_1"}}`)
	})
	_, err := c.Data.From("todos").Select("").Into(context.Background(), nil)
	e, ok := err.(*Error)
	if !ok || e.Status != 403 || e.Code != "rls_required" || e.RequestID != "req_1" || !IsCode(err, "rls_required") {
		t.Fatalf("error: %#v", err)
	}
}

func TestRefreshOnce(t *testing.T) {
	var refreshes atomic.Int32
	f, c := newFake(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch r.URL.Path {
		case "/auth/v1/signin/password":
			_ = json.NewEncoder(w).Encode(Session{AccessToken: "old", RefreshToken: "r1", ExpiresAt: time.Now().Add(30 * time.Second).Unix()})
		case "/auth/v1/token":
			refreshes.Add(1)
			time.Sleep(50 * time.Millisecond)
			if !strings.Contains(body, `"r1"`) {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(Session{AccessToken: "fresh", RefreshToken: "r2", ExpiresAt: time.Now().Add(time.Hour).Unix()})
		default:
			_, _ = io.WriteString(w, `{"data":[]}`)
		}
	})
	ctx := context.Background()
	if _, err := c.Auth.SignInWithPassword(ctx, Credentials{Email: "a@b.c", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Data.From("t").Select("").Into(ctx, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("%d refreshes", refreshes.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.calls {
		if strings.HasPrefix(r.URL.Path, "/data/") && r.Header.Get("Authorization") != "Bearer fresh" {
			t.Fatalf("a read carried %q", r.Header.Get("Authorization"))
		}
	}
}

func TestVerifyToken(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := key.PublicKey.Bytes()
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := map[string]any{"keys": []map[string]string{{"kid": "k1", "kty": "EC", "crv": "P-256", "x": b64(pub[1:33]), "y": b64(pub[33:])}}}
	_, c := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ string) { _ = json.NewEncoder(w).Encode(jwks) })
	sign := func(claims map[string]any) string {
		h, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": "k1", "typ": "JWT"})
		p, _ := json.Marshal(claims)
		in := b64(h) + "." + b64(p)
		sum := sha256.Sum256([]byte(in))
		r, s, _ := ecdsa.Sign(rand.Reader, key, sum[:])
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return in + "." + b64(sig)
	}
	ctx := context.Background()
	cl, err := c.Auth.VerifyToken(ctx, sign(map[string]any{"sub": "u1", "role": "user", "exp": time.Now().Add(time.Hour).Unix()}))
	if err != nil || cl.Subject() != "u1" || cl.Role() != "user" {
		t.Fatalf("verify: %v %v", cl, err)
	}
	if _, err := c.Auth.VerifyToken(ctx, sign(map[string]any{"sub": "u1", "exp": time.Now().Add(-time.Minute).Unix()})); err == nil {
		t.Fatal("an expired token verified")
	}
	tok := sign(map[string]any{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	if _, err := c.Auth.VerifyToken(ctx, tok[:len(tok)-4]+"AAAA"); err == nil {
		t.Fatal("a forged signature verified")
	}
}
