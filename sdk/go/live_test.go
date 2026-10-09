package pgdock

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLive runs against a real edge when TestSDKs (test/integration)
// sets PGDOCK_SDK_URL.
func TestLive(t *testing.T) {
	base := os.Getenv("PGDOCK_SDK_URL")
	if base == "" {
		t.Skip("PGDOCK_SDK_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := New(base, os.Getenv("PGDOCK_SDK_PUB"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Auth.SignInWithPassword(ctx, Credentials{Email: "go@sdk.test", Password: os.Getenv("PGDOCK_SDK_PASSWORD")})
	if err != nil || s == nil {
		t.Fatalf("sign in: %v", err)
	}
	uid := s.User.ID

	// A server verifies the token with the JWKS.
	srv, _ := New(base, os.Getenv("PGDOCK_SDK_SECRET"))
	claims, err := srv.Auth.VerifyToken(ctx, s.AccessToken)
	if err != nil || claims.Subject() != uid || claims.Role() != "user" {
		t.Fatalf("verify: %v %v", claims, err)
	}
	// And acts as the user with WithToken, on a publishable-key client.
	type note struct {
		ID      int64  `json:"id"`
		OwnerID string `json:"owner_id"`
		Body    string `json:"body"`
	}
	server, _ := New(base, os.Getenv("PGDOCK_SDK_PUB"))
	asUser := server.WithToken(s.AccessToken)
	if rows, _, err := List[note](ctx, asUser.Data.From("notes").Select("")); err != nil || len(rows) != 0 {
		t.Fatalf("as the user: %v %v", rows, err)
	}

	// Realtime.
	var mu sync.Mutex
	var got []Change
	ch := c.Realtime.Channel("notes-go").OnChange(ChangeFilter{Event: "INSERT", Table: "notes"}, func(ch Change) {
		mu.Lock()
		got = append(got, ch)
		mu.Unlock()
	})
	if err := ch.Subscribe(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer c.Realtime.Close()

	var ins []note
	if n, err := c.Data.From("notes").Insert(map[string]any{"body": "go 1"}).Exec(ctx, &ins); err != nil || n != 1 || ins[0].OwnerID != uid {
		t.Fatalf("insert: %d %v %v", n, ins, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		ok := len(got) > 0 && strings.Contains(string(got[0].Record), `"go 1"`)
		mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no INSERT change")
		}
		time.Sleep(100 * time.Millisecond)
	}
	rows, page, err := List[note](ctx, c.Data.From("notes").Select("id,body,owner_id").Count("exact"))
	if err != nil || len(rows) != 1 || rows[0].Body != "go 1" || page.Count == nil || *page.Count != 1 {
		t.Fatalf("read: %v %+v %v", rows, page, err)
	}

	// Refresh.
	s2, err := c.Auth.Refresh(ctx)
	if err != nil || s2.AccessToken == s.AccessToken {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := c.Data.From("notes").Select("").Into(ctx, nil); err != nil {
		t.Fatalf("read after refresh: %v", err)
	}

	// Files.
	b := c.Storage.Bucket("files")
	body := []byte("hello from go")
	if _, err := b.Upload(ctx, uid+"/hello.txt", bytes.NewReader(body), int64(len(body)), UploadOptions{ContentType: "text/plain"}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	rc, _, err := b.Download(ctx, uid+"/hello.txt", "")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	got2, _ := io.ReadAll(rc)
	rc.Close()
	if string(got2) != string(body) {
		t.Fatalf("downloaded %q", got2)
	}
	signed, err := b.SignedURL(ctx, uid+"/hello.txt", time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	su, _ := url.Parse(signed)
	bu, _ := url.Parse(base)
	su.Scheme, su.Host = bu.Scheme, bu.Host
	res, err := http.Get(su.String())
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("signed URL: %v %v", res, err)
	}
	res.Body.Close()
	if _, err := b.Upload(ctx, "someone-else/x.txt", strings.NewReader("no"), 2, UploadOptions{ContentType: "text/plain"}); !isStatus(err, 403) {
		t.Fatalf("upload into another folder: %v", err)
	}

	// Errors.
	if _, err := c.Data.From("nope").Select("").Into(ctx, nil); !IsCode(err, "unknown_table") || !isStatus(err, 404) {
		t.Fatalf("unknown table: %v", err)
	}

	if err := c.Auth.SignOut(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Auth.Session(ctx); s != nil {
		t.Fatal("still signed in")
	}
}

func isStatus(err error, status int) bool {
	e, ok := err.(*Error)
	return ok && e.Status == status
}
