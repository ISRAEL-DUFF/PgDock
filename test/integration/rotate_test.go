package integration

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/rotate"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestMasterKeyRotation covers spec §7.3: every stored secret is
// re-encrypted under the new key, after which the new key alone opens
// them all and the old one opens none.
func TestMasterKeyRotation(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	e.ConfigureBackups()
	if code := e.Do("PUT", "/api/v1/settings/alerts", map[string]any{
		"webhook_url": "https://hooks.example.com/x", "webhook_secret": "hook-secret",
		"smtp": map[string]any{"host": "smtp.example.com", "from": "pgdock@example.com", "to": []string{"ops@example.com"}, "password": "smtp-secret"},
	}, nil); code != http.StatusOK {
		t.Fatalf("alerts: %d", code)
	}
	e.CreateProject("Keyed")
	host, port, _ := strings.Cut(e.SMTP.Addr, ":")
	portN, _ := strconv.Atoi(port)
	if code := e.Do("PUT", "/api/v1/admin/settings/mail", map[string]any{
		"host": host, "port": portN, "tls": "none", "from": "pgdock@pgdock.test", "username": "u", "password": "smtp-secret",
		"test_to": "owner@example.com",
	}, nil); code != http.StatusOK {
		t.Fatalf("mail settings: %d", code)
	}

	newKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	both, err := crypto.NewKeyring(newKey, e.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	res, err := rotate.Run(ctx, e.DB, both)
	if err != nil {
		t.Fatal(err)
	}
	for _, what := range []string{"user TOTP secret", "recovery codes", "storage credentials", "node admin credential", "agent CA key", "backup key",
		"alert webhook secret", "alert SMTP password", "SMTP password"} {
		if res.Rewrapped[what] == 0 || res.Rewrapped[what] != res.Checked[what] {
			t.Errorf("%s: rewrapped %d of %d", what, res.Rewrapped[what], res.Checked[what])
		}
	}

	only, _ := crypto.NewKeyring(newKey)
	if _, err := rotate.Verify(ctx, e.DB, only); err != nil {
		t.Fatalf("new key alone: %v", err)
	}
	old, _ := crypto.NewKeyring(e.MasterKey)
	if _, err := rotate.Verify(ctx, e.DB, old); err == nil {
		t.Fatal("the old key still opens the secrets")
	}
	again, err := rotate.Run(ctx, e.DB, both)
	if err != nil {
		t.Fatal(err)
	}
	for what, n := range again.Rewrapped {
		if n > 0 {
			t.Errorf("second run rewrapped %d %s", n, what)
		}
	}
	// A wrong previous key rolls back without changing anything.
	stranger, _ := crypto.GenerateKey()
	wrong, _ := crypto.NewKeyring(stranger, stranger)
	if _, err := rotate.Run(ctx, e.DB, wrong); err == nil {
		t.Fatal("rotation with a key that opens nothing succeeded")
	}
	if _, err := rotate.Verify(ctx, e.DB, only); err != nil {
		t.Fatalf("after the failed rotation: %v", err)
	}
}
