package cloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

// Bootstrap is what a new server needs to join PGDock by itself (V3 §5.1):
// it installs Docker, hardens SSH, opens only the ports PGDock uses to the
// private network, and runs the agent, which registers with a one-time
// token.
type Bootstrap struct {
	// NodeName is the node the token was issued for.
	NodeName string
	// ServerURL is how the agent reaches pgdock-server (PGDOCK_AGENT_SERVER).
	ServerURL string
	// ServerCA is the PEM of a private CA that signed pgdock-server's
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
	// DBAllow and MoveAllow become PGDOCK_AGENT_DB_ALLOW and _MOVE_ALLOW
	// (default PrivateCIDR).
	DBAllow   string
	MoveAllow string
	// SSHKeys are public keys for root, besides the provider's.
	SSHKeys []string
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
		return fmt.Errorf("cloud-init: the agent and Postgres images must be pullable references (PGDOCK_CLOUD_AGENT_IMAGE, PGDOCK_CLOUD_PG_IMAGE)")
	case !cidrRe.MatchString(b.PrivateCIDR):
		return fmt.Errorf("cloud-init: the private network %q is not a CIDR", b.PrivateCIDR)
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
# PGDock node {{.NodeName}}: joins by itself with a one-time token (V3 §5.1).
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
  - path: /etc/ssh/sshd_config.d/90-pgdock.conf
    permissions: "0644"
    content: |
      PasswordAuthentication no
      KbdInteractiveAuthentication no
      PermitRootLogin prohibit-password
      X11Forwarding no
      MaxAuthTries 3
  - path: /etc/pgdock/agent.env
    permissions: "0600"
    content: |
      PGDOCK_AGENT_SERVER={{.ServerURL}}
      PGDOCK_AGENT_TOKEN={{.Token}}
      PGDOCK_AGENT_PG_IMAGE={{.PGImage}}
      PGDOCK_AGENT_DB_ALLOW={{.DBAllow}}
      PGDOCK_AGENT_MOVE_ALLOW={{.MoveAllow}}
      PGDOCK_AGENT_LOG_FORMAT=json
{{- if .ServerCA}}
      PGDOCK_AGENT_SERVER_CA=/etc/pgdock/server-ca.pem
  - path: /etc/pgdock/server-ca.pem
    permissions: "0644"
    content: |
{{indent 6 .ServerCA}}
{{- end}}
  - path: /usr/local/sbin/pgdock-join
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
      docker pull {{q .AgentImage}}
      docker rm -f pgdock-agent 2>/dev/null || true
      docker run -d --name pgdock-agent --restart unless-stopped --network host --user 0:0 \
        --env-file /etc/pgdock/agent.env -e PGDOCK_AGENT_ADVERTISE="$ip:7070" -e PGDOCK_AGENT_PUBLISH="$ip" \
        -v /var/lib/pgdock-agent:/var/lib/pgdock-agent -v /var/run/docker.sock:/var/run/docker.sock \
        {{- if .ServerCA}} -v /etc/pgdock/server-ca.pem:/etc/pgdock/server-ca.pem:ro{{end}} \
        {{q .AgentImage}}
runcmd:
  - [/usr/local/sbin/pgdock-join]
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
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PGDOCK_AGENT_TOKEN="); ok {
			return v
		}
	}
	return ""
}
