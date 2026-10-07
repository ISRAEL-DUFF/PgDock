package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/docker"
)

// The node's etcd member (V3 §2.2): one container per node, its data in a
// volume, TLS for clients and peers with certificates from pgdock-server's
// etcd CA, and /health on a plain-HTTP metrics port for this agent.
const (
	etcdClientPort  = 2379
	etcdPeerPort    = 2380
	etcdMetricsPort = 2381
	etcdCertDir     = "/etc/pgdock-etcd"
	etcdDataDir     = "/var/lib/etcd"
	// DefaultEtcdImage is the official etcd image.
	DefaultEtcdImage = "gcr.io/etcd-development/etcd:v3.6.5"
)

func (s *Service) etcdNames() (container, volume string) {
	return "pgdock-etcd-" + s.cfg.NodeID, "pgdock-etcd-" + s.cfg.NodeID
}

// peerMode reports how other nodes reach containers here: on the node's
// published address (fixed ports), or by name on the agent's Docker
// network when the agent publishes only on loopback, or not at all (one
// Docker host, as in development).
func (in *instances) peerMode() (publish bool, err error) {
	if a := in.cfg.PublishAddr; a != "" {
		if ip := net.ParseIP(a); ip == nil || !ip.IsLoopback() {
			return true, nil
		}
	}
	if in.cfg.Network != "" {
		return false, nil
	}
	return false, errors.New("other nodes can't reach this node's containers: set PGDOCK_AGENT_PUBLISH to the node's private address, or PGDOCK_AGENT_NETWORK")
}

func (s *Service) etcdAddress() (agentapi.EtcdAddress, error) {
	publish, err := s.inst.peerMode()
	if err != nil {
		return agentapi.EtcdAddress{}, err
	}
	name, _ := s.etcdNames()
	host := name
	if publish {
		host = s.inst.cfg.PublishAddr
	}
	return agentapi.EtcdAddress{Host: host, ClientPort: etcdClientPort, PeerPort: etcdPeerPort}, nil
}

// metricsURL is where this agent reaches the member's /health.
func (s *Service) metricsURL() (string, error) {
	publish, err := s.inst.peerMode()
	if err != nil {
		return "", err
	}
	name, _ := s.etcdNames()
	if publish {
		return "http://127.0.0.1:" + strconv.Itoa(etcdMetricsPort), nil
	}
	return "http://" + name + ":" + strconv.Itoa(etcdMetricsPort), nil
}

func (s *Service) etcdAddressHandler(w http.ResponseWriter, _ *http.Request) {
	a, err := s.etcdAddress()
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

var regexpEtcdName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func validEtcdSpec(spec agentapi.EtcdSpec) error {
	switch {
	case !regexpEtcdName.MatchString(spec.Name):
		return fmt.Errorf("bad member name %q", spec.Name)
	case spec.State != "new" && spec.State != "existing":
		return fmt.Errorf("state must be new or existing")
	case spec.InitialCluster == "" || strings.ContainsAny(spec.InitialCluster, " \n"):
		return fmt.Errorf("bad initial cluster")
	case !regexpEtcdName.MatchString(spec.Token):
		return fmt.Errorf("bad cluster token")
	case spec.CAPEM == "" || spec.CertPEM == "" || spec.KeyPEM == "":
		return fmt.Errorf("the member's TLS certificate, key and CA are required")
	}
	return nil
}

func (s *Service) runEtcd(w http.ResponseWriter, r *http.Request) {
	if s.inst.err != nil {
		fail(w, http.StatusServiceUnavailable, s.inst.err)
		return
	}
	var spec agentapi.EtcdSpec
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&spec); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := validEtcdSpec(spec); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.startEtcd(r.Context(), spec)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Service) startEtcd(ctx context.Context, spec agentapi.EtcdSpec) (agentapi.Etcd, error) {
	in := s.inst
	name, vol := s.etcdNames()
	defer in.lock(name)()
	addr, err := s.etcdAddress()
	if err != nil {
		return agentapi.Etcd{}, err
	}
	publish, _ := in.peerMode()
	// Re-created each time, so the flags and certificates are current; the
	// volume keeps the member's data unless asked to wipe it.
	if err := in.dc.RemoveContainer(ctx, name); err != nil {
		return agentapi.Etcd{}, err
	}
	if spec.Wipe {
		if err := in.dc.RemoveVolume(ctx, vol); err != nil {
			return agentapi.Etcd{}, err
		}
	}
	image := in.cfg.EtcdImage
	if image == "" {
		image = DefaultEtcdImage
	}
	if ok, err := in.dc.ImageExists(ctx, image); err != nil {
		return agentapi.Etcd{}, err
	} else if !ok {
		if err := in.dc.Pull(ctx, image); err != nil {
			return agentapi.Etcd{}, err
		}
	}
	labels := map[string]string{"pgdock.etcd": s.cfg.NodeID}
	if err := in.dc.CreateVolume(ctx, vol, labels); err != nil {
		return agentapi.Etcd{}, err
	}
	url := func(port int) string { return "https://" + net.JoinHostPort(addr.Host, strconv.Itoa(port)) }
	cmd := []string{
		"/usr/local/bin/etcd",
		"--name=" + spec.Name,
		"--data-dir=" + etcdDataDir,
		"--listen-client-urls=https://0.0.0.0:" + strconv.Itoa(etcdClientPort),
		"--advertise-client-urls=" + url(addr.ClientPort),
		"--listen-peer-urls=https://0.0.0.0:" + strconv.Itoa(etcdPeerPort),
		"--initial-advertise-peer-urls=" + url(addr.PeerPort),
		"--listen-metrics-urls=http://0.0.0.0:" + strconv.Itoa(etcdMetricsPort),
		"--initial-cluster=" + spec.InitialCluster,
		"--initial-cluster-state=" + spec.State,
		"--initial-cluster-token=" + spec.Token,
		"--client-cert-auth", "--trusted-ca-file=" + etcdCertDir + "/ca.pem",
		"--cert-file=" + etcdCertDir + "/member.pem", "--key-file=" + etcdCertDir + "/member-key.pem",
		"--peer-client-cert-auth", "--peer-trusted-ca-file=" + etcdCertDir + "/ca.pem",
		"--peer-cert-file=" + etcdCertDir + "/member.pem", "--peer-key-file=" + etcdCertDir + "/member-key.pem",
		// Patroni's few keys need little: keep the history short.
		"--auto-compaction-mode=periodic", "--auto-compaction-retention=1h", "--quota-backend-bytes=1073741824",
	}
	cc := docker.ContainerConfig{
		Image: image, Cmd: cmd, Labels: labels,
		HostConfig: docker.HostConfig{
			Mounts:        []docker.Mount{{Type: "volume", Source: vol, Target: etcdDataDir}},
			RestartPolicy: &docker.RestartPolicy{Name: "unless-stopped"},
			Memory:        512 << 20,
		},
	}
	if publish {
		p := func(port int, host string) []docker.PortBinding {
			return []docker.PortBinding{{HostIP: host, HostPort: strconv.Itoa(port)}}
		}
		cc.ExposedPorts = map[string]struct{}{"2379/tcp": {}, "2380/tcp": {}, "2381/tcp": {}}
		cc.HostConfig.PortBindings = map[string][]docker.PortBinding{
			"2379/tcp": p(etcdClientPort, in.cfg.PublishAddr),
			"2380/tcp": p(etcdPeerPort, in.cfg.PublishAddr),
			"2381/tcp": p(etcdMetricsPort, "127.0.0.1"),
		}
	}
	if in.cfg.Network != "" {
		cc.HostConfig.NetworkMode = in.cfg.Network
		cc.Networking = &docker.NetworkingConfig{EndpointsConfig: map[string]docker.EndpointSettings{in.cfg.Network: {Aliases: []string{name}}}}
	}
	if _, err := in.dc.CreateContainer(ctx, name, cc); err != nil {
		return agentapi.Etcd{}, err
	}
	if err := in.dc.CopyTo(ctx, name, "/etc", []docker.File{
		{Name: "pgdock-etcd/", Mode: 0o700},
		{Name: "pgdock-etcd/ca.pem", Mode: 0o644, Data: []byte(spec.CAPEM)},
		{Name: "pgdock-etcd/member.pem", Mode: 0o644, Data: []byte(spec.CertPEM)},
		{Name: "pgdock-etcd/member-key.pem", Mode: 0o600, Data: []byte(spec.KeyPEM)},
	}); err != nil {
		_ = in.dc.RemoveContainer(context.WithoutCancel(ctx), name)
		return agentapi.Etcd{}, fmt.Errorf("etcd certificates: %w", err)
	}
	if err := in.dc.StartContainer(ctx, name); err != nil {
		return agentapi.Etcd{}, err
	}
	return s.etcdState(ctx), nil
}

// etcdState reports the container and, if it runs, the member's health. A
// member of a cluster without quorum reports unhealthy.
func (s *Service) etcdState(ctx context.Context) agentapi.Etcd {
	name, _ := s.etcdNames()
	out := agentapi.Etcd{Container: name, State: "missing"}
	out.Address, _ = s.etcdAddress()
	c, err := s.inst.dc.InspectContainer(ctx, name)
	if err != nil {
		if !errors.Is(err, docker.ErrNotFound) {
			out.Error = err.Error()
		}
		return out
	}
	out.State, out.Running = c.State.Status, c.State.Running
	if !out.Running {
		return out
	}
	u, err := s.metricsURL()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(hctx, http.MethodGet, u+"/health", nil)
	res, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer res.Body.Close()
	var h struct {
		Health string `json:"health"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&h)
	out.Healthy = res.StatusCode == http.StatusOK && h.Health == "true"
	if !out.Healthy {
		out.Error = strings.TrimSpace("unhealthy " + h.Reason)
	}
	return out
}

func (s *Service) getEtcd(w http.ResponseWriter, r *http.Request) {
	if s.inst.err != nil {
		fail(w, http.StatusServiceUnavailable, s.inst.err)
		return
	}
	writeJSON(w, http.StatusOK, s.etcdState(r.Context()))
}

func (s *Service) removeEtcd(w http.ResponseWriter, r *http.Request) {
	if s.inst.err != nil {
		fail(w, http.StatusServiceUnavailable, s.inst.err)
		return
	}
	name, vol := s.etcdNames()
	defer s.inst.lock(name)()
	if err := s.inst.dc.RemoveContainer(r.Context(), name); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.inst.dc.RemoveVolume(r.Context(), vol); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
