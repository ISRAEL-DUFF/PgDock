package faildomain

import (
	"testing"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
)

func node(name, domain, group string) store.Node {
	n := store.Node{ID: uuid.New(), Name: name}
	if domain != "" {
		n.FailureDomain = &domain
	}
	if group != "" {
		n.PlacementGroup = &group
	}
	return n
}

func TestSeparated(t *testing.T) {
	a, b, c := node("a", "rack-a", ""), node("b", "rack-a", ""), node("c", "rack-b", "")
	alone1, alone2 := node("x", "", ""), node("y", "", "")
	g1, g1b, g2 := node("h1", "", "11"), node("h2", "", "11"), node("h3", "", "22")
	for _, tc := range []struct {
		name string
		x, y store.Node
		want bool
	}{
		{"same rack", a, b, false},
		{"other rack", a, c, true},
		{"the same node", a, a, false},
		{"neither recorded", alone1, alone2, true},
		{"one recorded, one alone", a, alone1, true},
		{"one spread group", g1, g1b, true},
		{"two spread groups", g1, g2, false},
		{"recorded beats group", node("r1", "rack-a", "11"), node("r2", "rack-a", "11"), false},
	} {
		if got := Separated(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
		if Separated(tc.x, tc.y) != Separated(tc.y, tc.x) {
			t.Errorf("%s: not symmetric", tc.name)
		}
	}
	if ok, pair := AllSeparated([]store.Node{a, c, b}); ok || pair[0].Name != "a" || pair[1].Name != "b" {
		t.Errorf("all separated: %v %s %s", ok, pair[0].Name, pair[1].Name)
	}
	if ok, _ := AllSeparated([]store.Node{a, c, alone1}); !ok {
		t.Error("three domains reported as shared")
	}
}
