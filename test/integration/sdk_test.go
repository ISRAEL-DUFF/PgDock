package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestSDKs runs each SDK's live test (sdk/js/test/live.mjs,
// sdk/go/live_test.go, sdk/dart/test/live_test.dart) against a project on
// a real edge: sign-in and refresh, reads and writes under row-level
// security, a realtime change, files, errors and sign-out. The edge is
// reached through a local proxy that gives it the project's host name.
// Node and Go always run; Dart runs when `dart` is on PATH or
// PGDOCK_TEST_DART names it.
func TestSDKs(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.ConfigureBackups() // file storage
	ctx := context.Background()
	creds := e.CreateProject("sdk-app")
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
	ed := e.StartEdge()
	waitFor(t, 30*time.Second, "the edge reaches the project", func() bool {
		code, _, _ := ed.Do(ref, "GET", "/data/v1/health", nil, "apikey", pub)
		return code == 200
	})

	app := e.MustConnect(creds.Connection.PooledUrl)
	defer app.Close(ctx)
	user := store.UserRole(p.DbName)
	for _, st := range []string{
		`CREATE TABLE notes (id bigserial PRIMARY KEY, owner_id uuid NOT NULL DEFAULT pgd_auth.uid() REFERENCES pgd_auth.users (id) ON DELETE CASCADE,
			body text NOT NULL, created_at timestamptz NOT NULL DEFAULT now())`,
		`ALTER TABLE notes ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own ON notes FOR ALL TO %q USING (owner_id = pgd_auth.uid()) WITH CHECK (owner_id = pgd_auth.uid())`, user),
		`SELECT pgd_realtime.enable('notes')`,
		fmt.Sprintf(`CREATE POLICY own_files ON pgd_storage.objects FOR ALL TO %q
			USING (bucket = 'files' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text)
			WITH CHECK (bucket = 'files' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text)`, user),
	} {
		if _, err := app.Exec(ctx, st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	admin := authAPI{t: t, ed: ed, ref: ref, key: sec}
	for _, who := range []string{"js", "go", "dart", "other"} {
		if r := admin.post("/auth/v1/admin/users", fmt.Sprintf(`{"email":"%s@sdk.test","password":"sdk-password-123","email_confirm":true}`, who)); r.Code != 201 {
			t.Fatalf("user %s: %d %s", who, r.Code, r.Body)
		}
	}
	// Someone else's note, which no SDK user may see.
	other := authAPI{t: t, ed: ed, ref: ref, key: pub}.post("/auth/v1/signin/password", `{"email":"other@sdk.test","password":"sdk-password-123"}`)
	if code, _, body := ed.Do(ref, "POST", "/data/v1/notes", strings.NewReader(`{"body":"not yours"}`), "apikey", pub,
		"Authorization", "Bearer "+other.Session.AccessToken, "Content-Type", "application/json"); code != 201 {
		t.Fatalf("other's note: %d %s", code, body)
	}
	if code, _, body := ed.Do(ref, "POST", "/storage/v1/bucket", strings.NewReader(`{"id":"files"}`), "apikey", sec, "Content-Type", "application/json"); code != 201 {
		t.Fatalf("bucket: %d %s", code, body)
	}

	// The edge under the project's host name, at a local address the SDKs reach.
	target, _ := url.Parse(ed.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = ref + "." + testenv.EdgeDomain
		r.Header.Set("X-Forwarded-Proto", "http")
	}
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	env := append(os.Environ(), "PGDOCK_SDK_URL="+srv.URL, "PGDOCK_SDK_PUB="+pub, "PGDOCK_SDK_SECRET="+sec, "PGDOCK_SDK_PASSWORD=sdk-password-123")
	run := func(t *testing.T, dir string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = filepath.Join(root, dir)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		t.Logf("%s", out)
	}

	t.Run("js", func(t *testing.T) {
		if _, err := exec.LookPath("node"); err != nil {
			t.Skip("node not installed")
		}
		if _, err := os.Stat(filepath.Join(root, "sdk/js/node_modules/.bin/tsc")); err != nil {
			run(t, "sdk/js", "npm", "install", "--no-audit", "--no-fund")
		}
		run(t, "sdk/js", "npx", "tsc", "-p", "tsconfig.json")
		run(t, "sdk/js", "node", "test/live.mjs")
	})
	t.Run("go", func(t *testing.T) {
		run(t, "sdk/go", "go", "test", "-count=1", "-race", "-run", "TestLive", "./...")
	})
	t.Run("dart", func(t *testing.T) {
		dart := os.Getenv("PGDOCK_TEST_DART")
		if dart == "" {
			if dart, err = exec.LookPath("dart"); err != nil {
				t.Skip("dart not installed (set PGDOCK_TEST_DART)")
			}
		}
		run(t, "sdk/dart", dart, "pub", "get")
		run(t, "sdk/dart", dart, "test", "test/live_test.dart")
	})
}
