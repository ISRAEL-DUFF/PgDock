package integration

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/floatip"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// edgeHost is one pooler-host container and its keepalived sidecar.
type edgeHost struct {
	name, container, ip, serverID string
}

// TestPoolerHostFailover is M17's done-when (V3 §2.1): two real pooler
// hosts (the pooler-host image) with keepalived on the dev Docker network,
// a shared address keepalived moves, and a fake Hetzner API for the
// floating IP. Killing the host that holds the address must bring
// connections back through it, on the other host, within 10 seconds.
func TestPoolerHostFailover(t *testing.T) {
	hostImage, kaImage := os.Getenv("PGDOCK_TEST_POOLER_HOST_IMAGE"), os.Getenv("PGDOCK_TEST_KEEPALIVED_IMAGE")
	if hostImage == "" || kaImage == "" {
		t.Skip("PGDOCK_TEST_POOLER_HOST_IMAGE and PGDOCK_TEST_KEEPALIVED_IMAGE are not set (make pooler-host-images)")
	}
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	gw := os.Getenv("PGDOCK_TEST_DOCKER_GATEWAY")
	network := os.Getenv("PGDOCK_TEST_DOCKER_NETWORK")
	if gw == "" || network == "" {
		t.Skip("PGDOCK_TEST_DOCKER_GATEWAY and PGDOCK_TEST_DOCKER_NETWORK are not set")
	}
	prefix := gw[:strings.LastIndex(gw, ".")+1]
	vip := prefix + "240"

	// The fake Hetzner API, where the agents (in containers) and the
	// arbiter (here) reach it.
	fake := &floatip.Fake{Token: "edge-token", IPID: "77"}
	ln, err := net.Listen("tcp", net.JoinHostPort(gw, "0"))
	if err != nil {
		t.Fatal(err)
	}
	hz := &http.Server{Handler: fake, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hz.Serve(ln) }()
	t.Cleanup(func() { _ = hz.Close() })
	hzURL := "http://" + ln.Addr().String() + "/v1"

	hosts := []*edgeHost{
		{name: "edge-a", container: "pgdock-test-edge-a", ip: prefix + "231", serverID: "1001"},
		{name: "edge-b", container: "pgdock-test-edge-b", ip: prefix + "232", serverID: "1002"},
	}
	removeEdge := func() {
		for _, h := range hosts {
			_ = exec.Command("docker", "rm", "-f", "-v", h.container+"-keepalived", h.container).Run()
		}
	}
	removeEdge()
	t.Cleanup(func() {
		if t.Failed() {
			for _, h := range hosts {
				for _, c := range []string{h.container, h.container + "-keepalived"} {
					out, _ := exec.Command("docker", "logs", "--tail", "40", c).CombinedOutput()
					t.Logf("%s:\n%s", c, out)
				}
			}
		}
		removeEdge()
		_, _ = e.DB.Exec(context.Background(), `DELETE FROM nodes WHERE role = 'pooler'`)
		_, _ = e.DB.Exec(context.Background(), `DELETE FROM pooler_config`)
	})

	keepalivedScript, err := os.ReadFile("../../deploy/pooler-host/keepalived.sh")
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range hosts {
		var created gen.NodeCreated
		if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: h.name, PrivateAddr: h.ip, Role: gen.CreateNodeRequestRolePooler}, &created); code != http.StatusCreated || created.Token == "" {
			t.Fatalf("create pooler node %s: %d", h.name, code)
		}
		edgeDir := t.TempDir()
		if err := os.Chmod(edgeDir, 0o777); err != nil {
			t.Fatal(err)
		}
		peer := hosts[1-i].ip
		run := []string{"run", "-d", "--name", h.container, "--network", network, "--ip", h.ip,
			"-v", edgeDir + ":/etc/pgdock-edge",
			"-e", "PGDOCK_AGENT_SERVER=" + e.URL, "-e", "PGDOCK_AGENT_TOKEN=" + created.Token,
			"-e", "PGDOCK_AGENT_ADVERTISE=" + h.ip + ":7070", "-e", "PGDOCK_AGENT_LOG_FORMAT=text",
			"-e", "PGDOCK_AGENT_SERVER_ID=" + h.serverID, "-e", "PGDOCK_AGENT_HETZNER_API=" + hzURL,
			"-e", "PGDOCK_AGENT_HETZNER_TOKEN=" + fake.Token, "-e", "PGDOCK_AGENT_FLOATING_IP_ID=" + fake.IPID,
			"-e", "PGDOCK_EDGE_SELF_IP=" + h.ip, "-e", "PGDOCK_EDGE_PEER_IP=" + peer, "-e", "PGDOCK_EDGE_VIP=" + vip,
			"-e", "PGDOCK_EDGE_VRRP_PASSWORD=edgetest", "-e", "PGDOCK_EDGE_PRIORITY=" + strconv.Itoa(110-10*i),
			hostImage}
		if out, err := exec.Command("docker", run...).CombinedOutput(); err != nil {
			t.Fatalf("start %s: %v: %s", h.container, err, out)
		}
		ka := []string{"run", "-d", "--name", h.container + "-keepalived", "--network", "container:" + h.container,
			"--cap-add", "NET_ADMIN", "--cap-add", "NET_BROADCAST", "--cap-add", "NET_RAW",
			"-v", edgeDir + ":/etc/pgdock-edge:ro", "--entrypoint", "sh", kaImage, "-c", string(keepalivedScript)}
		if out, err := exec.Command("docker", ka...).CombinedOutput(); err != nil {
			t.Fatalf("start %s keepalived: %v: %s", h.container, err, out)
		}
	}

	q := store.New(e.DB)
	waitFor(t, 60*time.Second, "both pooler hosts to register", func() bool {
		for _, h := range hosts {
			n, err := q.GetNodeByName(ctx, h.name)
			if err != nil || n.AgentCertFp == nil {
				return false
			}
		}
		return true
	})

	// pgdock-server's side, as cmd/server wires it.
	e.Pooler.SetHostDriver(nodes.PoolerDriver{S: e.Nodes}, pooler.HostAdminConfig{
		User: "pgdock", Password: os.Getenv("PGDOCK_TEST_POOLER_ADMIN_PASSWORD"), SSLMode: "require",
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	arb := pooler.NewArbiter(e.Pooler, &floatip.Hetzner{API: hzURL, Token: fake.Token, IPID: fake.IPID}, log)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); arb.Run(actx, 3*time.Second) }()
	t.Cleanup(func() { stop(); <-done })

	creds := e.CreateProject("edge")
	if err := e.Pooler.Reload(ctx); err != nil {
		t.Fatalf("push to pooler hosts: %v", err)
	}
	u, err := url.Parse(creds.Connection.PooledUrl)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = net.JoinHostPort(vip, "6543")
	viaVIP := u.String()
	query := func() error {
		cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		c, err := pgx.Connect(cctx, viaVIP)
		if err != nil {
			return err
		}
		defer c.Close(context.Background())
		var one int
		return c.QueryRow(cctx, "SELECT 1").Scan(&one)
	}

	byServer := map[int64]*edgeHost{1001: hosts[0], 1002: hosts[1]}
	var lastErr error
	waitFor(t, 60*time.Second, "an active pooler host serving through the shared address", func() bool {
		snap := arb.Snapshot()
		ready := 0
		for _, h := range snap.Hosts {
			if h.Ready {
				ready++
			}
		}
		lastErr = query()
		return ready == 2 && byServer[fake.Assigned()] != nil && lastErr == nil
	})
	active := byServer[fake.Assigned()]
	standby := hosts[0]
	if active == hosts[0] {
		standby = hosts[1]
	}
	t.Logf("active %s, standby %s", active.name, standby.name)

	// Kill the active host: no clean shutdown, as on a crashed machine.
	start := time.Now()
	if out, err := exec.Command("docker", "kill", active.container).CombinedOutput(); err != nil {
		t.Fatalf("kill %s: %v: %s", active.container, err, out)
	}
	for {
		lastErr = query()
		if lastErr == nil && byServer[fake.Assigned()] == standby {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("no recovery within 10s: last query error %v, floating IP on %d", lastErr, fake.Assigned())
		}
		time.Sleep(200 * time.Millisecond)
	}
	took := time.Since(start)
	t.Logf("recovered on %s in %s", standby.name, took.Round(100*time.Millisecond))

	// The arbiter saw keepalived's move and recorded it.
	n, err := q.GetNodeByName(ctx, standby.name)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "a took_ip event for the standby", func() bool {
		evs, err := q.ListPoolerEvents(ctx, 50)
		if err != nil {
			return false
		}
		for _, ev := range evs {
			if ev.Kind == "took_ip" && ev.NodeID != nil && *ev.NodeID == n.ID {
				return true
			}
		}
		return false
	})
	if snap := arb.Snapshot(); snap.HolderName != standby.name {
		t.Fatalf("arbiter sees the floating IP on %q, want %s", snap.HolderName, standby.name)
	}
}
