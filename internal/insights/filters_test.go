package insights

import (
	"slices"
	"testing"
)

func TestFilterColumns(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"(customer_id = 42)", []string{"customer_id"}},
		{"(orders.customer_id = $1)", []string{"customer_id"}},
		{"((status)::text = 'open'::text)", []string{"status"}},
		{"((orders.status)::text = 'open'::text)", []string{"status"}},
		{"((customer_id = 7) AND (created_at > '2026-01-01'::date))", []string{"customer_id", "created_at"}},
		{`("Email" ~~ 'a%'::text)`, []string{"Email"}},
		{"(deleted_at IS NULL)", []string{"deleted_at"}},
	} {
		if got := filterColumns(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.in, got, c.want)
		}
	}
}
