package nodes

import (
	"errors"
	"fmt"
	"strings"

	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/version"
)

// ErrIncompatibleAgent means a node's agent runs a different major version
// than pgdock-server (spec §11.3: no work is scheduled on it).
var ErrIncompatibleAgent = errors.New("incompatible agent version")

// major returns the major version of a release like "v1.2.3" or "1.2.3",
// and false for development builds ("dev", a commit).
func major(v string) (string, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	m, rest, found := strings.Cut(v, ".")
	if !found || m == "" || rest == "" {
		return "", false
	}
	for _, c := range m {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return m, true
}

// Compatible reports whether an agent version may take work from a server
// version: same major version; development builds match anything.
func Compatible(server, agent string) bool {
	sm, sok := major(server)
	am, aok := major(agent)
	return !sok || !aok || sm == am
}

// checkVersion refuses a node whose agent last reported an incompatible
// version.
func checkVersion(n store.Node) error {
	if n.AgentVersion == nil {
		return nil
	}
	server := version.Get().Version
	if !Compatible(server, *n.AgentVersion) {
		return fmt.Errorf("%w: the agent on %s runs %s and pgdock-server runs %s; upgrade the agent (docs/upgrade.md)",
			ErrIncompatibleAgent, n.Name, *n.AgentVersion, server)
	}
	return nil
}
