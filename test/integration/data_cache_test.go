package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
)

// TestDataCache is V4.1-M9's done-when (V4.1 §10): a listed table's second
// anonymous read is a HIT; a user's read is never cached; an insert through
// the API drops the entry; an SQL insert shows within the TTL; requests are
// metered either way.
func TestDataCache(t *testing.T) {
	sp := newStorageProject(t, "shop", false)
	e, ctx := sp.e, context.Background()
	adm := sp.adminConn(t)
	defer adm.Close(ctx)
	anon, user, service := store.AnonRole(sp.db), store.UserRole(sp.db), store.ServiceRole(sp.db)
	for _, stmt := range []string{
		`CREATE TABLE public.products (id serial PRIMARY KEY, name text NOT NULL)`,
		`ALTER TABLE public.products ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY readable ON public.products FOR SELECT USING (true)`,
		fmt.Sprintf(`GRANT SELECT ON public.products TO %q, %q, %q`, anon, user, service),
		fmt.Sprintf(`GRANT INSERT ON public.products TO %q`, service),
		fmt.Sprintf(`GRANT USAGE ON SEQUENCE public.products_id_seq TO %q`, service),
		`INSERT INTO public.products (name) VALUES ('tea')`,
		`CREATE FUNCTION public.product_count() RETURNS bigint LANGUAGE sql STABLE AS 'SELECT count(*) FROM public.products'`,
	} {
		if _, err := adm.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	const ttl = 3
	ttls := map[string]int{"public.products": ttl, "rpc.product_count": ttl}
	if code := e.Do("PATCH", "/api/v1/projects/"+sp.pid.String()+"/services",
		gen.BackendServicesUpdate{Settings: &gen.BackendServicesSettings{CacheTtlSeconds: &ttls}}, nil); code != http.StatusOK {
		t.Fatalf("cache settings: %d", code)
	}
	bad := map[string]int{"products": 60}
	if code := e.Do("PATCH", "/api/v1/projects/"+sp.pid.String()+"/services",
		gen.BackendServicesUpdate{Settings: &gen.BackendServicesSettings{CacheTtlSeconds: &bad}}, nil); code != http.StatusBadRequest {
		t.Fatalf("an unqualified table name: %d", code)
	}

	const list = "/data/v1/products?select=id,name&order=id:asc"
	reads := 0
	read := func(path, token string, headers ...string) fileResult {
		t.Helper()
		reads++
		r := sp.anon.do("GET", path, nil, token, headers...)
		if r.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, r.Code, r.Body)
		}
		return r
	}
	// The edge picks the setting up from the feed.
	waitFor(t, 30*time.Second, "the cache setting reaching the edge", func() bool {
		return read(list, "").Header.Get("X-Cache") != ""
	})
	time.Sleep((ttl + 1) * time.Second) // start from an empty entry

	first := read(list, "")
	second := read(list, "")
	if first.Header.Get("X-Cache") != "MISS" || second.Header.Get("X-Cache") != "HIT" || string(first.Body) != string(second.Body) {
		t.Fatalf("first %q, second %q:\n%s\n%s", first.Header.Get("X-Cache"), second.Header.Get("X-Cache"), first.Body, second.Body)
	}
	if cc := second.Header.Get("Cache-Control"); !strings.HasPrefix(cc, "public, max-age=") || second.Header.Get("Age") == "" ||
		!strings.Contains(strings.Join(second.Header.Values("Vary"), ","), "Authorization") {
		t.Fatalf("a hit's headers: %v", second.Header)
	}
	// The same query with its parameters in another order is the same entry.
	if r := read("/data/v1/products?order=id:asc&select=id,name", ""); r.Header.Get("X-Cache") != "HIT" {
		t.Fatalf("reordered parameters: %q", r.Header.Get("X-Cache"))
	}

	// Never cached: a user's read, the secret key, Read-Replica: primary.
	for name, r := range map[string]fileResult{
		"a user's token": read(list, sp.alice),
		"the secret key": func() fileResult { reads++; return sp.admin.do("GET", list, nil, "") }(),
		"primary":        read(list, "", "Read-Replica", "primary"),
	} {
		if r.Header.Get("X-Cache") != "" || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s was cached: %v", name, r.Header)
		}
	}

	// An insert through the API drops the entry at once.
	if r := sp.admin.json("POST", "/data/v1/products", `{"name":"coffee"}`, ""); r.Code != http.StatusCreated {
		t.Fatalf("insert: %d %s", r.Code, r.Body)
	}
	if r := read(list, ""); r.Header.Get("X-Cache") != "MISS" || !strings.Contains(string(r.Body), "coffee") {
		t.Fatalf("after an API insert: %q %s", r.Header.Get("X-Cache"), r.Body)
	}

	// An SQL insert shows within the TTL: stale until then.
	if _, err := adm.Exec(ctx, `INSERT INTO public.products (name) VALUES ('cocoa')`); err != nil {
		t.Fatal(err)
	}
	if r := read(list, ""); r.Header.Get("X-Cache") != "HIT" || strings.Contains(string(r.Body), "cocoa") {
		t.Fatalf("right after an SQL insert: %q %s", r.Header.Get("X-Cache"), r.Body)
	}
	waitFor(t, (ttl+2)*time.Second, "the SQL insert within the TTL", func() bool {
		return strings.Contains(string(read(list, "").Body), "cocoa")
	})

	// Functions too, and a write drops them.
	if r := read("/data/v1/rpc/product_count", ""); r.Header.Get("X-Cache") != "MISS" {
		t.Fatalf("rpc first: %q", r.Header.Get("X-Cache"))
	}
	if r := read("/data/v1/rpc/product_count", ""); r.Header.Get("X-Cache") != "HIT" || !strings.Contains(string(r.Body), "3") {
		t.Fatalf("rpc second: %q %s", r.Header.Get("X-Cache"), r.Body)
	}
	if r := sp.admin.json("POST", "/data/v1/products", `{"name":"milo"}`, ""); r.Code != http.StatusCreated {
		t.Fatalf("insert: %d", r.Code)
	}
	if r := read("/data/v1/rpc/product_count", ""); r.Header.Get("X-Cache") != "MISS" || !strings.Contains(string(r.Body), "4") {
		t.Fatalf("rpc after a write: %q %s", r.Header.Get("X-Cache"), r.Body)
	}

	// Metered either way: every read is in the log and the request count.
	sp.ed.Flush(ctx)
	var logged int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM api_request_logs WHERE project_id = $1 AND method = 'GET' AND path LIKE '/data/v1/%'`,
		sp.pid).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	var requests float64
	if err := e.DB.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::float8 FROM usage_records WHERE project_id = $1 AND metric = 'api_requests'`,
		sp.pid).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if logged < reads || requests < float64(reads) {
		t.Fatalf("%d reads made, %d logged, %v metered", reads, logged, requests)
	}
}
