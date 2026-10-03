package integration

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestBackupDownload covers V2 §10.10's data export: an organisation owner
// downloads a backup as a plain pg_dump archive, whichever key encrypts
// it; nobody else can.
func TestBackupDownload(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	c := e.CreateProject("Exported")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	if _, err := app.Exec(ctx, `CREATE TABLE exported_items AS SELECT g AS id FROM generate_series(1, 100) g`); err != nil {
		t.Fatal(err)
	}
	app.Close(ctx)

	latest := func() gen.Backup {
		t.Helper()
		op := backupNow(t, e, pid)
		if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("backup: %s", op.Status)
		}
		var list gen.BackupList
		e.Do("GET", "/api/v1/backups?project_id="+pid, nil, &list)
		for _, b := range list.Items {
			if b.OperationId != nil && *b.OperationId == op.Id {
				return b
			}
		}
		t.Fatal("the backup is not listed")
		return gen.Backup{}
	}
	download := func(b gen.Backup) {
		t.Helper()
		code, text := e.GetText("/api/v1/backups/"+b.Id.String()+"/download", nil, true)
		body := []byte(text)
		if code != http.StatusOK || !bytes.HasPrefix(body, []byte("PGDMP")) || !bytes.Contains(body, []byte("exported_items")) {
			t.Fatalf("download of a %s-key backup: %d, %d bytes", deref((*string)(b.Encryption)), code, len(body))
		}
	}
	download(latest())
	var key gen.ProjectBackupKey
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/backup-key", nil, &key); code != http.StatusOK || !key.Enabled {
		t.Fatalf("enable the project key: %d", code)
	}
	b := latest()
	if b.Encryption == nil || *b.Encryption != gen.BackupEncryptionProject {
		t.Fatalf("encryption: %v", b.Encryption)
	}
	download(b)
	var audited int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'backup.download' AND outcome = 'success'`).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audited downloads: %d %v", audited, err)
	}

	// A developer on the project may not; a member not on it doesn't see it.
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/members", map[string]any{"email": "dev@example.com", "role": "developer"}, nil); code != http.StatusOK {
		t.Fatalf("invite: %d", code)
	}
	dev := e.AcceptInvitation(e.MailToken("dev@example.com", "invitation"), "dev@example.com")
	if code, _ := dev.DoRaw("GET", "/api/v1/backups/"+b.Id.String()+"/download", nil); code != http.StatusForbidden {
		t.Fatalf("a developer downloading: %d", code)
	}
	other := e.CreateProject("Elsewhere")
	if code := e.Do("POST", "/api/v1/projects/"+other.Project.Id.String()+"/members", map[string]any{"email": "outsider@example.com", "role": "admin"}, nil); code != http.StatusOK {
		t.Fatalf("invite: %d", code)
	}
	outsider := e.AcceptInvitation(e.MailToken("outsider@example.com", "invitation"), "outsider@example.com")
	if code, _ := outsider.DoRaw("GET", "/api/v1/backups/"+b.Id.String()+"/download", nil); code != http.StatusNotFound {
		t.Fatalf("a member not on the project downloading: %d", code)
	}
}
