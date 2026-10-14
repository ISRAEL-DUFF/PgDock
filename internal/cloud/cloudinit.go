package cloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

// Bootstrap is what a new server needs to join Xenibase by itself (V3 §5.1):
// it installs Docker, hardens SSH, opens only the ports Xenibase uses to the
// private network, and runs the agent, which registers with a one-time
// token.
type Bootstrap struct {
	// NodeName is the node the token was issued for.
	NodeName string
	// ServerURL is how the agent reaches xenibase-server (XENIBASE_AGENT_SERVER).
	ServerURL string
	// ServerCA is the PEM of a private CA that signed xenibase-server's
	// certificate, when it isn't publicly trusted.
	ServerCA string
	// Token is the node's one-time registration token.
	Token string
	// AgentImage and PGImage are pullable image references.
	AgentImage string
	PGImage    string
	// PrivateCIDR is the private network: the agent's and instances' ports
	// are open to it only, and the agent advertises its address on it.
	PrivateCIDR string
	// DBAllow and MoveAllow become XENIBASE_AGENT_DB_ALLOW and _MOVE_ALLOW
	// (default PrivateCIDR).
	DBAllow   string
	MoveAllow string
	// SSHKeys are public keys for root, besides the provider's.
	SSHKeys []string
	// Edge makes the server an edge node: xenibase-edge runs beside the agent
	// (V4.1 §11).
	Edge *EdgeBootstrap
}

// EdgeBootstrap is what an edge node's xenibase-edge needs.
type EdgeBootstrap struct {
	// Image is xenibase-edge's pullable image (XENIBASE_CLOUD_EDGE_IMAGE).
	Image string
	// ControlURL is xenibase-server as edges reach it, Secret the edge
	// secret, Domain the API domain, Region the region it serves.
	ControlURL string
	Secret     string
	Domain     string
	Region     string
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func (e EdgeBootstrap) validate() error {
	switch {
	case !imageRe.MatchString(e.Image):
		return fmt.Errorf("cloud-init: the edge image must be a pullable reference (XENIBASE_CLOUD_EDGE_IMAGE)")
	case !strings.HasPrefix(e.ControlURL, "https://") && !strings.HasPrefix(e.ControlURL, "http://"):
		return fmt.Errorf("cloud-init: the edge's control URL must be http(s)")
	case len(e.Secret) < 32 || strings.ContainsAny(e.Secret, " \n'\"$\\`"):
		return fmt.Errorf("cloud-init: the edge secret must be at least 32 characters without spaces or quotes")
	case !domainRe.MatchString(e.Domain):
		return fmt.Errorf("cloud-init: the API domain %q", e.Domain)
	case !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`).MatchString(e.Region):
		return fmt.Errorf("cloud-init: the region %q", e.Region)
	}
	return nil
}

var (
	cidrRe  = regexp.MustCompile(`^[0-9a-fA-F.:]+/[0-9]{1,3}$`)
	imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@{}-]*$`)
)

// Validate checks the values that go into shell commands.
func (b Bootstrap) Validate() error {
	switch {
	case !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(b.NodeName):
		return fmt.Errorf("cloud-init: node name %q", b.NodeName)
	case !strings.HasPrefix(b.ServerURL, "https://") && !strings.HasPrefix(b.ServerURL, "http://"):
		return fmt.Errorf("cloud-init: the server URL must be http(s)")
	case b.Token == "" || strings.ContainsAny(b.Token, " \n'\"$\\`"):
		return fmt.Errorf("cloud-init: a registration token is required")
	case !imageRe.MatchString(b.AgentImage) || !imageRe.MatchString(b.PGImage):
		return fmt.Errorf("cloud-init: the agent and Postgres images must be pullable references (XENIBASE_CLOUD_AGENT_IMAGE, XENIBASE_CLOUD_PG_IMAGE)")
	case !cidrRe.MatchString(b.PrivateCIDR):
		return fmt.Errorf("cloud-init: the private network %q is not a CIDR", b.PrivateCIDR)
	}
	if b.Edge != nil {
		if err := b.Edge.validate(); err != nil {
			return err
		}
	}
	for _, c := range []string{b.DBAllow, b.MoveAllow} {
		for _, p := range strings.Split(c, ",") {
			if p != "" && !cidrRe.MatchString(strings.TrimSpace(p)) {
				return fmt.Errorf("cloud-init: %q is not a CIDR", p)
			}
		}
	}
	return nil
}

// q quotes s for YAML (a JSON string is a YAML string).
func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var cloudInit = template.Must(template.New("cloud-init").Funcs(template.FuncMap{"q": q, "indent": func(n int, s string) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+pad)
}}).Parse(`#cloud-config
# Xenibase node {{.NodeName}}: joins by itself with a one-time token (V3 §5.1).
hostname: {{q .NodeName}}
package_update: true
package_upgrade: true
packages: [docker.io, ufw, fail2ban, unattended-upgrades]
{{- if .SSHKeys}}
ssh_authorized_keys:
{{- range .SSHKeys}}
  - {{q .}}
{{- end}}
{{- end}}
write_files:
  - path: /etc/ssh/sshd_config.d/90-xenibase.conf
    permissions: "0644"
    content: |
      PasswordAuthentication no
      KbdInteractiveAuthentication no
      PermitRootLogin prohibit-password
      X11Forwarding no
      MaxAuthTries 3
  - path: /etc/xenibase/agent.env
    permissions: "0600"
    content: |
      XENIBASE_AGENT_SERVER={{.ServerURL}}
      XENIBASE_AGENT_TOKEN={{.Token}}
      XENIBASE_AGENT_PG_IMAGE={{.PGImage}}
      XENIBASE_AGENT_DB_ALLOW={{.DBAllow}}
      XENIBASE_AGENT_MOVE_ALLOW={{.MoveAllow}}
      XENIBASE_AGENT_LOG_FORMAT=json
{{- if .ServerCA}}
      XENIBASE_AGENT_SERVER_CA=/etc/xenibase/server-ca.pem
  - path: /etc/xenibase/server-ca.pem
    permissions: "0644"
    content: |
{{indent 6 .ServerCA}}
{{- end}}
{{- if .Edge}}
  - path: /etc/xenibase/edge.env
    permissions: "0600"
    content: |
      XENIBASE_EDGE_NAME={{.NodeName}}
      XENIBASE_EDGE_CONTROL_URL={{.Edge.ControlURL}}
      XENIBASE_EDGE_SECRET={{.Edge.Secret}}
      XENIBASE_EDGE_DOMAIN={{.Edge.Domain}}
      XENIBASE_EDGE_REGION={{.Edge.Region}}
      XENIBASE_EDGE_LISTEN=:8443
      XENIBASE_EDGE_LOG_FORMAT=json
{{- end}}
  - path: /usr/local/sbin/xenibase-join
    permissions: "0700"
    content: |
      #!/bin/sh
      set -eu
      # The address on the private network, which the agent advertises
      # and Postgres instances publish on.
      net={{q .PrivateCIDR}}
      ip=$(ip -4 -o addr show | awk -v n="${net%%.*}." '$4 ~ "^"n {split($4,a,"/"); print a[1]; exit}')
      [ -n "$ip" ] || { echo "no address on $net" >&2; exit 1; }
      ufw default deny incoming
      ufw default allow outgoing
      ufw allow 22/tcp
      ufw allow from "$net"
      ufw --force enable
      systemctl restart ssh || systemctl restart sshd
      systemctl enable --now docker
{{- if .Edge}}
      # An edge node (V4.1 §11): xenibase-edge serves the API on 8443, for the
      # load balancer or TLS terminator in front.
      ufw allow 8443/tcp
      docker pull {{q .Edge.Image}}
      docker rm -f xenibase-edge 2>/dev/null || true
      docker run -d --name xenibase-edge --restart unless-stopped --network host --env-file /etc/xenibase/edge.env \
        {{- if .ServerCA}} -v /etc/xenibase/server-ca.pem:/etc/xenibase/server-ca.pem:ro{{end}} \
        {{q .Edge.Image}}
{{- end}}
      docker pull {{q .AgentImage}}
      docker rm -f xenibase-agent 2>/dev/null || true
      docker run -d --name xenibase-agent --restart unless-stopped --network host --user 0:0 \
        --env-file /etc/xenibase/agent.env -e XENIBASE_AGENT_ADVERTISE="$ip:7070" -e XENIBASE_AGENT_PUBLISH="$ip" \
        -v /var/lib/xenibase-agent:/var/lib/xenibase-agent -v /var/run/docker.sock:/var/run/docker.sock \
        {{- if .ServerCA}} -v /etc/xenibase/server-ca.pem:/etc/xenibase/server-ca.pem:ro{{end}} \
        {{q .AgentImage}}
runcmd:
  - [/usr/local/sbin/xenibase-join]
`))

// CloudInit renders the server's cloud-init document.
func (b Bootstrap) CloudInit() (string, error) {
	if b.DBAllow == "" {
		b.DBAllow = b.PrivateCIDR
	}
	if b.MoveAllow == "" {
		b.MoveAllow = b.PrivateCIDR
	}
	if err := b.Validate(); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := cloudInit.Execute(&buf, b); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// TokenFromCloudInit reads the registration token back out of a rendered
// document (the fake provider's tests boot "servers" with it).
func TokenFromCloudInit(doc string) string {
	for _, line := range strings.Split(doc, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "XENIBASE_AGENT_TOKEN="); ok {
			return v
		}
	}
	return ""
}
