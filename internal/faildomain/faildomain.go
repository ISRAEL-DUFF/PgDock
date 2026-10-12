// Package faildomain decides whether two nodes can fail together (V3.1
// §2). A node's failure domain is what the admin recorded (a rack, a host,
// a power feed). A Hetzner server in a spread placement group is on a
// different physical host from every other server in that group, and only
// those; a node with neither is its own domain, as it was before V3.1.
package faildomain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/israel-duff/pgdock/internal/store"
)

// Label is the node's domain as shown to admins.
func Label(n store.Node) string {
	switch {
	case n.FailureDomain != nil && *n.FailureDomain != "":
		return *n.FailureDomain
	case n.PlacementGroup != nil && *n.PlacementGroup != "":
		return "placement group " + *n.PlacementGroup
	default:
		return "its own (" + n.Name + ")"
	}
}

// Separated reports whether a and b are known not to fail together.
func Separated(a, b store.Node) bool {
	if a.ID == b.ID {
		return false
	}
	da, db := domain(a), domain(b)
	if da != "" || db != "" {
		// A recorded domain wins; a node without one is alone.
		if da == "" || db == "" {
			return true
		}
		return da != db
	}
	ga, gb := group(a), group(b)
	if ga != "" && gb != "" {
		// Spread: only servers within one group are kept apart.
		return ga == gb
	}
	return true
}

// AllSeparated reports whether every pair in ns is separated, and the
// first pair that isn't.
func AllSeparated(ns []store.Node) (bool, [2]store.Node) {
	for i := range ns {
		for j := i + 1; j < len(ns); j++ {
			if !Separated(ns[i], ns[j]) {
				return false, [2]store.Node{ns[i], ns[j]}
			}
		}
	}
	return true, [2]store.Node{}
}

// Describe names the nodes' domains, for error messages.
func Describe(ns []store.Node) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, fmt.Sprintf("%s: %s", n.Name, Label(n)))
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func domain(n store.Node) string {
	if n.FailureDomain == nil {
		return ""
	}
	return *n.FailureDomain
}

func group(n store.Node) string {
	if n.PlacementGroup == nil {
		return ""
	}
	return *n.PlacementGroup
}
