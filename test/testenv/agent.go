package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
)

// StartAgent runs pgdock-agent in the agent-test container (Postgres 18
// client tools, on the Compose network) and registers it for the shared
// node with a one-time token, as an operator would. It stops when the test
// ends.
func (e *Env) StartAgent() {
	e.t.Helper()
	container := need(e.t, "PGDOCK_TEST_AGENT_CONTAINER")
	agentAddr := need(e.t, "PGDOCK_TEST_AGENT_ADDR")
	ctx := context.Background()
	node, err := store.New(e.DB).GetNodeByName(ctx, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	token, _, err := e.Nodes.NewToken(ctx, node.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	stopAgent(container) // a leftover from an aborted run
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	state := "/tmp/agent-state-" + hex.EncodeToString(b)
	script := `echo $$ > /tmp/agent.pid; exec /pgdock/pgdock-agent run --state ` + state +
		` --server ` + e.URL + ` --token "$PGDOCK_AGENT_TOKEN" --advertise ` + agentAddr + ` --listen :7070 > /tmp/agent.log 2>&1`
	cmd := exec.Command("docker", "exec", "-d", "-e", "PGDOCK_AGENT_TOKEN="+token, "-e", "PGDOCK_AGENT_LOG_FORMAT=text", container, "sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("start agent: %v: %s", err, out)
	}
	e.t.Cleanup(func() {
		if e.t.Failed() {
			out, _ := exec.Command("docker", "exec", container, "tail", "-n", "40", "/tmp/agent.log").CombinedOutput()
			e.t.Logf("agent log:\n%s", out)
		}
		stopAgent(container)
	})

	deadline := time.Now().Add(60 * time.Second)
	for {
		n, err := store.New(e.DB).GetNodeByName(ctx, "test")
		if err == nil && n.AgentCertFp != nil {
			if st := e.Nodes.Check(ctx, n); st.Reachable {
				return
			} else if time.Now().After(deadline) {
				e.t.Fatalf("agent registered but unreachable: %s", st.Err)
			}
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("docker", "exec", container, "cat", "/tmp/agent.log").CombinedOutput()
			e.t.Fatalf("agent did not register in time; log:\n%s", out)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func stopAgent(container string) {
	_ = exec.Command("docker", "exec", container, "sh", "-c",
		`[ -f /tmp/agent.pid ] && kill $(cat /tmp/agent.pid) 2>/dev/null; rm -f /tmp/agent.pid; sleep 0.3`).Run()
}

// ConfigureBackups starts a fake S3 the agent can reach, saves it through
// the API (which runs the live test), and generates and confirms the
// backup key. It returns the key as downloaded.
func (e *Env) ConfigureBackups() string {
	e.t.Helper()
	gw := os.Getenv("PGDOCK_TEST_DOCKER_GATEWAY")
	addr := ""
	if gw != "" {
		addr = net.JoinHostPort(gw, "0")
	}
	fake, err := storage.NewFakeAt("pgdock-test", addr)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(fake.Close)
	e.S3 = fake
	tgt := fake.Target("pgdock-test")
	var res gen.StorageTestResult
	region, prefix, pathStyle := tgt.Region, "pgdock", true
	if code := e.Do("PUT", "/api/v1/settings/storage", gen.StorageRequest{
		Endpoint: tgt.Endpoint, Region: &region, Bucket: tgt.Bucket, Prefix: &prefix,
		AccessKey: tgt.AccessKey, SecretKey: &tgt.SecretKey, PathStyle: &pathStyle,
	}, &res); code != http.StatusOK || !res.Saved {
		e.t.Fatalf("save storage: status %d, %+v", code, res)
	}
	var key gen.BackupKeyExport
	if code := e.Do("POST", "/api/v1/settings/backup-key", nil, &key); code != http.StatusCreated || !strings.HasPrefix(key.Key, "pgdock-backup-key-v1:") {
		e.t.Fatalf("generate backup key: status %d", code)
	}
	var info gen.BackupKeyInfo
	if code := e.Do("POST", "/api/v1/settings/backup-key/confirm", gen.BackupKeyConfirmRequest{Key: key.Key}, &info); code != http.StatusOK || info.ConfirmedAt == nil {
		e.t.Fatalf("confirm backup key: status %d", code)
	}
	return key.Key
}
