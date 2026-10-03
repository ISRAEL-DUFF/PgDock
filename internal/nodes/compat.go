package nodes

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/version"
)

// ErrIncompatibleAgent means a node's agent runs a different major version
// than pgdock-server, or an older minor (spec §11.3: no work is scheduled on it).
var ErrIncompatibleAgent = errors.New("incompatible agent version")

// parse returns the major and minor of a release like "v1.2.3" or "1.2.3",
// and false for development builds ("dev", a commit).
func parse(v string) (major, minor int, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	return major, minor, err1 == nil && err2 == nil && major >= 0 && minor >= 0
}

// Compatible reports whether an agent version may take work from a server
// version: the same major version, and not an older minor, since a server
// sends work an older agent doesn't know how to do. A newer minor is fine
// (agents are upgraded before the server), and development builds match
// anything.
func Compatible(server, agent string) bool {
	sm, sn, sok := parse(server)
	am, an, aok := parse(agent)
	return !sok || !aok || (sm == am && an >= sn)
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
