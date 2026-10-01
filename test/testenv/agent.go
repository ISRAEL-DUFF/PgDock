package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
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
	node, err := store.New(e.DB).GetNodeByName(context.Background(), "test")
	if err != nil {
		e.t.Fatal(err)
	}
	token, _, err := e.Nodes.NewToken(context.Background(), node.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	e.runAgent(container, agentAddr, "test", token)
}

// StartSecondAgent runs a second agent (the agent-test-2 container) for
// the node named name, registered with token (from POST /nodes).
func (e *Env) StartSecondAgent(name, token string) {
	e.t.Helper()
	e.runAgent(need(e.t, "PGDOCK_TEST_AGENT2_CONTAINER"), need(e.t, "PGDOCK_TEST_AGENT2_ADDR"), name, token)
}

func (e *Env) runAgent(container, agentAddr, nodeName, token string) {
	e.t.Helper()
	ctx := context.Background()
	stopAgent(container) // a leftover from an aborted run
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	state := "/tmp/agent-state-" + hex.EncodeToString(b)
	script := `echo $$ > /tmp/agent.pid; exec /pgdock/pgdock-agent run --state ` + state +
		` --server ` + e.URL + ` --token "$PGDOCK_AGENT_TOKEN" --advertise ` + agentAddr + ` --listen :7070 > /tmp/agent.log 2>&1`
	args := []string{"exec", "-d", "-e", "PGDOCK_AGENT_TOKEN=" + token, "-e", "PGDOCK_AGENT_LOG_FORMAT=text"}
	if img := os.Getenv("PGDOCK_TEST_PG_IMAGE"); img != "" {
		// Instances join the Compose network (poolers and the agent use the
		// container name) and publish on 127.0.0.1 (the test server).
		args = append(args, "-e", "PGDOCK_AGENT_PG_IMAGE="+img, "-e", "PGDOCK_AGENT_NETWORK="+os.Getenv("PGDOCK_TEST_DOCKER_NETWORK"),
			"-e", "PGDOCK_AGENT_PUBLISH=127.0.0.1")
	}
	cmd := exec.Command("docker", append(args, container, "sh", "-c", script)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("start agent: %v: %s", err, out)
	}
	if e.agentRun == nil {
		e.agentRun = map[string][]string{}
	}
	e.agentRun[nodeName] = append(append([]string(nil), args...), container, "sh", "-c", script)
	e.t.Cleanup(func() {
		if e.t.Failed() {
			out, _ := exec.Command("docker", "exec", container, "tail", "-n", "40", "/tmp/agent.log").CombinedOutput()
			e.t.Logf("agent log:\n%s", out)
		}
		stopAgent(container)
		removeInstances(e.t)
	})

	deadline := time.Now().Add(60 * time.Second)
	for {
		n, err := store.New(e.DB).GetNodeByName(ctx, nodeName)
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
	if e.s3Link {
		u, err := url.Parse(tgt.Endpoint)
		if err != nil {
			e.t.Fatal(err)
		}
		e.S3Link = NewLink(e.t, addr, u.Host)
		u.Host = e.S3Link.Addr
		tgt.Endpoint = u.String()
	}
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

// removeInstances deletes instance containers and volumes a test left
// behind (normally deletes and rollbacks remove them).
func removeInstances(t testing.TB) {
	for _, kind := range []string{"container", "volume"} {
		list := []string{"ps", "-aq"}
		if kind == "volume" {
			list = []string{"volume", "ls", "-q"}
		}
		out, err := exec.Command("docker", append(list, "--filter", "label=pgdock.instance")...).Output()
		if err != nil {
			continue
		}
		ids := strings.Fields(string(out))
		if len(ids) == 0 {
			continue
		}
		t.Logf("removing %d leftover instance %s(s)", len(ids), kind)
		rm := []string{"rm", "-f"}
		if kind == "volume" {
			rm = []string{"volume", "rm", "-f"}
		}
		_ = exec.Command("docker", append(rm, ids...)...).Run()
	}
}

// SetNodeRole changes a node's role (the harness registers "test" as a
// shared node).
func (e *Env) SetNodeRole(name, role string) {
	e.t.Helper()
	if _, err := e.DB.Exec(context.Background(), `UPDATE nodes SET role = $2 WHERE name = $1`, name, role); err != nil {
		e.t.Fatal(err)
	}
}

// KillAgent kills the agent of node with SIGKILL, as a crash would (the
// state directory survives).
func (e *Env) KillAgent(node string) {
	e.t.Helper()
	run := e.agentRun[node]
	if run == nil {
		e.t.Fatalf("no agent started for %s", node)
	}
	container := run[len(run)-4]
	if out, err := exec.Command("docker", "exec", container, "sh", "-c", `kill -9 $(cat /tmp/agent.pid)`).CombinedOutput(); err != nil {
		e.t.Fatalf("kill agent: %v: %s", err, out)
	}
}

// RestartAgent starts a killed agent again with its saved state and waits
// until it answers.
func (e *Env) RestartAgent(node string) {
	e.t.Helper()
	run := e.agentRun[node]
	if out, err := exec.Command("docker", run...).CombinedOutput(); err != nil {
		e.t.Fatalf("restart agent: %v: %s", err, out)
	}
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for {
		n, err := store.New(e.DB).GetNodeByName(ctx, node)
		if err == nil {
			if st := e.Nodes.Check(ctx, n); st.Reachable {
				return
			} else if time.Now().After(deadline) {
				e.t.Fatalf("agent did not come back: %s", st.Err)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}
