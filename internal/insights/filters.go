package insights

import (
	"regexp"
	"slices"
	"strings"
)

// A column compared in a plan's Filter, e.g. (customer_id = 42) or
// ((status)::text = 'open'::text).
var filterCol = regexp.MustCompile(`\(+(?:"?[A-Za-z_][A-Za-z0-9_$]*"?\.)?"?([A-Za-z_][A-Za-z0-9_$]*)"?\)?(?:::[a-z ]+)?\s*(?:=|<>|<=|>=|<|>|~~\*?|!~~|IS NOT NULL|IS NULL)`)

// filterColumns are the columns a Filter expression compares, in order,
// without duplicates.
func filterColumns(f string) []string {
	var out []string
	for _, m := range filterCol.FindAllStringSubmatch(f, -1) {
		c := m[1]
		if l := strings.ToLower(c); l == "and" || l == "or" || l == "not" || slices.Contains(out, c) {
			continue
		}
		out = append(out, c)
	}
	return out
}
