package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestDataAPIIdempotencyKey is Taskiem's P1-G2: a write repeated with the
// same Idempotency-Key gets the first answer and writes nothing more, the
// same key with a different request is refused, a key is a caller's own,
// and a write that rolled back left no key behind.
func TestDataAPIIdempotencyKey(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	creds := e.CreateProject("idempotent")
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
	app := e.MustConnect(creds.Connection.PooledUrl)
	defer app.Close(ctx)
	for _, stmt := range []string{
		`CREATE TABLE orders (id bigserial PRIMARY KEY, owner uuid, status text NOT NULL CHECK (status <> 'bad'))`,
		`ALTER TABLE orders ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own ON orders FOR ALL TO %q USING (owner = pgd_auth.uid()) WITH CHECK (owner = pgd_auth.uid())`, store.UserRole(p.DbName)),
		`CREATE FUNCTION bump() RETURNS bigint LANGUAGE sql AS 'INSERT INTO orders (status) VALUES (''bumped'') RETURNING id'`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	ed := e.StartEdge()
	type res struct {
		code     int
		replayed bool
		body     string
		err      string
	}
	send := func(key, method, path, body, idem string, bearer ...string) res {
		t.Helper()
		h := []string{"apikey", key, "Content-Type", "application/json"}
		if idem != "" {
			h = append(h, "Idempotency-Key", idem)
		}
		if len(bearer) > 0 {
			h = append(h, "Authorization", "Bearer "+bearer[0])
		}
		code, hdr, out := ed.Do(ref, method, path, strings.NewReader(body), h...)
		var er struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal([]byte(out), &er)
		return res{code, hdr.Get("Idempotent-Replayed") == "true", out, er.Error.Code}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := app.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// ---- An insert, repeated: the first answer, one row ------------------------------
	k1 := uuid.NewString()
	first := send(sec, "POST", "/data/v1/orders", `{"status":"new"}`, k1)
	if first.code != http.StatusCreated || first.replayed {
		t.Fatalf("first insert: %+v", first)
	}
	again := send(sec, "POST", "/data/v1/orders", `{"status":"new"}`, k1)
	if again.code != http.StatusCreated || !again.replayed || again.body != first.body || count() != 1 {
		t.Fatalf("the repeat: %+v (first %s), %d rows", again, first.body, count())
	}
	// The same key for something else is refused, and writes nothing.
	if r := send(sec, "POST", "/data/v1/orders", `{"status":"other"}`, k1); r.code != http.StatusUnprocessableEntity || r.err != "idempotency_key_reused" || count() != 1 {
		t.Fatalf("a reused key: %+v", r)
	}
	if r := send(sec, "POST", "/data/v1/orders?return=minimal", `{"status":"new"}`, k1); r.err != "idempotency_key_reused" {
		t.Fatalf("a reused key with other parameters: %+v", r)
	}
	// Without a key, the same body writes again.
	if r := send(sec, "POST", "/data/v1/orders", `{"status":"new"}`, ""); r.code != http.StatusCreated || count() != 2 {
		t.Fatalf("no key: %+v", r)
	}

	// ---- A write that failed left no key: a retry runs ----------------------------------
	k2 := uuid.NewString()
	if r := send(sec, "POST", "/data/v1/orders", `{"status":"bad"}`, k2); r.code != http.StatusUnprocessableEntity || r.err != "check_violation" {
		t.Fatalf("a failing insert: %+v", r)
	}
	if _, err := app.Exec(ctx, `ALTER TABLE orders DROP CONSTRAINT orders_status_check`); err != nil {
		t.Fatal(err)
	}
	if r := send(sec, "POST", "/data/v1/orders", `{"status":"bad"}`, k2); r.code != http.StatusCreated || r.replayed || count() != 3 {
		t.Fatalf("the retry after the fix: %+v", r)
	}

	// ---- Updates, deletes, batches and functions --------------------------------------
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/data/v1/orders?where=status:eq:new", `{"status":"paid"}`},
		{"DELETE", "/data/v1/orders?where=status:eq:bad", ""},
		{"POST", "/data/v1/batch", `{"operations":[{"op":"insert","table":"orders","rows":{"status":"batched"}}]}`},
		{"POST", "/data/v1/rpc/bump", `{}`},
	} {
		k := uuid.NewString()
		before := count()
		a := send(sec, c.method, c.path, c.body, k)
		b := send(sec, c.method, c.path, c.body, k)
		if a.code >= 300 || a.replayed || !b.replayed || a.body != b.body || a.code != b.code {
			t.Fatalf("%s %s: %+v then %+v", c.method, c.path, a, b)
		}
		if c.method == "POST" && count() != before+1 {
			t.Fatalf("%s %s wrote twice: %d → %d", c.method, c.path, before, count())
		}
	}

	// ---- Concurrent repeats: one write -------------------------------------------------
	k3 := uuid.NewString()
	before := count()
	var wg sync.WaitGroup
	results := make([]res, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = send(sec, "POST", "/data/v1/orders", `{"status":"race"}`, k3)
		}()
	}
	wg.Wait()
	fresh := 0
	for _, r := range results {
		if r.code != http.StatusCreated || r.body != results[0].body {
			t.Fatalf("concurrent repeats: %+v", results)
		}
		if !r.replayed {
			fresh++
		}
	}
	if fresh != 1 || count() != before+1 {
		t.Fatalf("concurrent repeats: %d fresh answers, %d rows written", fresh, count()-before)
	}

	// ---- Keys are the caller's own -----------------------------------------------------
	u1, u2 := uuid.New(), uuid.New()
	tok := func(u uuid.UUID) string {
		s, err := e.Services.MintToken(ctx, pid, map[string]any{"sub": u.String(), "role": "user"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	k4 := uuid.NewString()
	if r := send(pub, "POST", "/data/v1/orders", fmt.Sprintf(`{"owner":"%s","status":"mine"}`, u1), k4, tok(u1)); r.code != http.StatusCreated || r.replayed {
		t.Fatalf("user 1: %+v", r)
	}
	// Another user with the same key and body: their own write, not user
	// 1's answer.
	if r := send(pub, "POST", "/data/v1/orders", fmt.Sprintf(`{"owner":"%s","status":"mine"}`, u1), k4, tok(u2)); r.replayed || r.err != "permission_denied" {
		t.Fatalf("user 2 with user 1's key: %+v", r)
	}
	if r := send(sec, "POST", "/data/v1/orders", fmt.Sprintf(`{"owner":"%s","status":"mine"}`, u1), k4); r.replayed || r.code != http.StatusCreated {
		t.Fatalf("the secret key with user 1's key: %+v", r)
	}
	// The publishable key without a user can't use keys.
	if r := send(pub, "POST", "/data/v1/orders", `{"status":"anon"}`, uuid.NewString()); r.code != http.StatusBadRequest || r.err != "idempotency_needs_user" {
		t.Fatalf("anon with a key: %+v", r)
	}
	if r := send(sec, "POST", "/data/v1/orders", `{}`, strings.Repeat("k", 256)); r.err != "invalid_idempotency_key" {
		t.Fatalf("a long key: %+v", r)
	}
	if r := send(sec, "POST", "/data/v1/orders", `{}`, "has space"); r.err != "invalid_idempotency_key" {
		t.Fatalf("a key with a space: %+v", r)
	}

	// ---- After a day, a key is free again ------------------------------------------------
	admin := e.SharedAdmin(p.DbName)
	if _, err := admin.Exec(ctx, `UPDATE pgd_auth.idempotency SET created_at = now() - interval '25 hours' WHERE key = $1`, k1); err != nil {
		t.Fatal(err)
	}
	before = count()
	if r := send(sec, "POST", "/data/v1/orders", `{"status":"other"}`, k1); r.code != http.StatusCreated || r.replayed || count() != before+1 {
		t.Fatalf("an expired key: %+v", r)
	}
	// The project's OpenAPI document says so.
	if code, _, doc := ed.Do(ref, "GET", "/data/v1/openapi.json", nil, "apikey", sec); code != http.StatusOK ||
		!strings.Contains(doc, `"Idempotency-Key"`) || !strings.Contains(doc, `"#/components/parameters/IdempotencyKey"`) {
		t.Fatalf("the OpenAPI document doesn't describe Idempotency-Key: %d", code)
	}
	// The request roles can't read the stored answers.
	if _, err := app.Exec(ctx, `SELECT * FROM pgd_auth.idempotency`); err == nil {
		t.Fatal("the project's owner can read the stored answers")
	}
}
