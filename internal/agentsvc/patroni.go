package agentsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/docker"
)

// HA members (V3 §2.2): the instance's container runs Patroni, which runs
// Postgres with the instance's settings and keeps the cluster's state in
// etcd.
const (
	restPort      = "8008/tcp"
	patroniLabel  = "pgdock.patroni"
	patroniConfig = "/etc/patroni"
)

// dcsParameters must match on every member, so Patroni keeps them in etcd
// rather than in each member's configuration.
var dcsParameters = []string{
	"max_connections", "max_locks_per_transaction", "max_worker_processes", "max_prepared_transactions",
	"wal_level", "track_commit_timestamp", "max_wal_senders", "max_replication_slots", "wal_keep_size", "wal_log_hints",
}

// memberPorts are the host ports an HA member is published on when other
// nodes reach it at the node's address: fixed for the container's life, so
// the addresses Patroni advertises stay true. 0 means not published that
// way.
type memberPorts struct{ pg, rest int }

// portsOf reads the ports a container was published on.
func portsOf(c docker.Container) memberPorts {
	get := func(p string) int {
		if b := c.NetworkSettings.Ports[p]; len(b) > 0 {
			n, _ := strconv.Atoi(b[0].HostPort)
			return n
		}
		return 0
	}
	return memberPorts{pg: get(pgPort), rest: get(restPort)}
}

// freePort asks the kernel for a free port on addr.
func freePort(addr string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(addr, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// applyPatroni turns cc, built for a plain instance with settings, into an
// HA member's container. prev are the ports of the container it replaces.
func (in *instances) applyPatroni(cc *docker.ContainerConfig, spec agentapi.InstanceSpec, name string, settings map[string]string, prev memberPorts) error {
	p := spec.Patroni
	if p.Scope == "" || len(p.Etcd) == 0 || p.ReplicationUser == "" || p.ReplicationPassword == "" || p.RestPassword == "" {
		return fmt.Errorf("patroni: scope, etcd, replication and REST credentials are required")
	}
	publish, err := in.peerMode()
	if err != nil {
		return err
	}
	host, pg, rest := name, 5432, 8008
	cc.ExposedPorts[restPort] = struct{}{}
	if publish {
		host = in.cfg.PublishAddr
		pg, rest = prev.pg, prev.rest
		if pg == 0 {
			if pg, err = freePort(host); err != nil {
				return err
			}
		}
		if rest == 0 {
			if rest, err = freePort(host); err != nil {
				return err
			}
		}
		cc.HostConfig.PortBindings = map[string][]docker.PortBinding{
			pgPort:   {{HostIP: host, HostPort: strconv.Itoa(pg)}},
			restPort: {{HostIP: host, HostPort: strconv.Itoa(rest)}},
		}
	} else if in.cfg.PublishAddr != "" {
		// One Docker host (development): peers use the network; the
		// published ports are for pgdock-server only.
		cc.HostConfig.PortBindings[restPort] = []docker.PortBinding{{HostIP: in.cfg.PublishAddr, HostPort: ""}}
	}
	var etcdHosts []string
	for _, e := range p.Etcd {
		u, err := url.Parse(e)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("patroni: bad etcd URL %q", e)
		}
		etcdHosts = append(etcdHosts, u.Host)
	}
	dcs := map[string]any{"wal_log_hints": "on", "hot_standby": "on", "max_wal_senders": "10", "max_replication_slots": "10"}
	local := map[string]any{}
	for k, v := range settings {
		if slices.Contains(dcsParameters, k) {
			dcs[k] = v
		} else {
			local[k] = v
		}
	}
	local["listen_addresses"] = "*"
	scram := func(typ, db, user, cidr string) string {
		return fmt.Sprintf("%s %s %s %s scram-sha-256", typ, db, user, cidr)
	}
	hba := []string{
		"local all all trust",
		scram("host", "all", "all", "127.0.0.1/32"),
		scram("host", "replication", p.ReplicationUser, "127.0.0.1/32"),
	}
	for _, c := range in.cfg.HBAAllow {
		hba = append(hba, scram("host", "all", "all", c))
	}
	for _, c := range p.PeerAllow {
		hba = append(hba, scram("host", "replication", p.ReplicationUser, c), scram("host", "all", spec.AdminUser, c))
	}
	for _, c := range in.cfg.MoveAllow {
		hba = append(hba, scram("host", "all", "/^pgdock_move_[0-9a-f]+$", c))
	}
	major := spec.PGVersion
	if major == 0 {
		major = agentapi.DefaultPGVersion
	}
	recovery := map[string]any{}
	if spec.WALG != nil {
		recovery["restore_command"] = "wal-g wal-fetch %f %p"
	}
	replicaMethods := []string{"basebackup"}
	methods := map[string]any{"basebackup": map[string]any{"checkpoint": "fast"}}
	if spec.WALG != nil {
		replicaMethods = []string{"walg", "basebackup"}
		methods["walg"] = map[string]any{"command": "/usr/local/bin/pgdock-patroni replica", "no_leader": true}
	}
	conf := map[string]any{
		"scope":     p.Scope,
		"namespace": "/pgdock/",
		"name":      spec.ID,
		"log":       map[string]any{"level": "INFO"},
		"restapi": map[string]any{
			"listen": "0.0.0.0:8008", "connect_address": net.JoinHostPort(host, strconv.Itoa(rest)),
			"authentication": map[string]any{"username": agentapi.PatroniREST, "password": p.RestPassword},
		},
		"etcd3": map[string]any{
			"hosts": etcdHosts, "protocol": "https",
			"cacert": patroniConfig + "/etcd-ca.pem", "cert": patroniConfig + "/etcd-cert.pem", "key": patroniConfig + "/etcd-key.pem",
		},
		"bootstrap": map[string]any{
			// Failover within about 20-30 seconds: the leader key's TTL,
			// plus a loop.
			"dcs": map[string]any{
				"ttl": 20, "loop_wait": 5, "retry_timeout": 5, "maximum_lag_on_failover": 1 << 20,
				"synchronous_mode": p.Synchronous,
				"postgresql":       map[string]any{"use_pg_rewind": true, "use_slots": true, "parameters": dcs},
			},
		},
		"postgresql": map[string]any{
			"listen": "0.0.0.0:5432", "connect_address": net.JoinHostPort(host, strconv.Itoa(pg)),
			"data_dir": pgdataFor(spec.PGVersion), "bin_dir": "/usr/lib/postgresql/" + strconv.Itoa(major) + "/bin",
			"pgpass": "/tmp/pgpass",
			"authentication": map[string]any{
				"superuser":   map[string]any{"username": spec.AdminUser, "password": spec.AdminPassword},
				"replication": map[string]any{"username": p.ReplicationUser, "password": p.ReplicationPassword},
				"rewind":      map[string]any{"username": spec.AdminUser, "password": spec.AdminPassword},
			},
			"parameters":             local,
			"pg_hba":                 hba,
			"recovery_conf":          recovery,
			"create_replica_methods": replicaMethods,
		},
		// Containers have no watchdog device: fencing is the leader lease
		// (a primary that loses it demotes itself).
		"watchdog": map[string]any{"mode": "off"},
	}
	for k, v := range methods {
		conf["postgresql"].(map[string]any)[k] = v
	}
	raw, err := json.Marshal(conf)
	if err != nil {
		return err
	}
	// Patroni, not the image's entry point, initialises and runs Postgres.
	cc.Env = slices.DeleteFunc(cc.Env, func(e string) bool {
		return strings.HasPrefix(e, "POSTGRES_") || strings.HasPrefix(e, "PGDOCK_HBA_ALLOW=")
	})
	cc.Env = append(cc.Env, "PGDOCK_PATRONI_CONFIG="+string(raw),
		"PGDOCK_ETCD_CA="+p.EtcdCA, "PGDOCK_ETCD_CERT="+p.EtcdCert, "PGDOCK_ETCD_KEY="+p.EtcdKey)
	cc.Entrypoint, cc.Cmd = []string{"/usr/local/bin/pgdock-patroni"}, nil
	cc.StopSignal = "SIGTERM" // Patroni stops Postgres cleanly
	cc.Labels[patroniLabel] = p.Scope
	return nil
}

// waitPatroni waits until Patroni's REST API answers in the container.
func (in *instances) waitPatroni(ctx context.Context, name string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		c, err := in.dc.InspectContainer(ctx, name)
		if err != nil {
			return err
		}
		if c.RestartCount >= 3 || (!c.State.Running && c.State.Status != "created" && c.State.Status != "restarting") {
			logs, _ := in.dc.Logs(ctx, name, 30)
			return fmt.Errorf("HA member %s is %s (exit %d): %s", name, c.State.Status, c.State.ExitCode, strings.TrimSpace(logs))
		}
		if c.State.Running {
			r, err := in.dc.Exec(ctx, name, "postgres", nil, []string{"/opt/patroni/bin/python", "-c",
				"import urllib.request; urllib.request.urlopen('http://127.0.0.1:8008/liveness', timeout=2)"})
			if err == nil && r.ExitCode == 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			logs, _ := in.dc.Logs(ctx, name, 30)
			return fmt.Errorf("HA member %s: Patroni did not start: %s", name, strings.TrimSpace(logs))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
