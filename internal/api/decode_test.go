package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/provision"
)

// A request that names a field the endpoint doesn't have is refused. Ignoring
// it is not harmless: PUT /admin/orgs/{org}/outbound with a misspelled
// "allowlist" instead of "hosts" used to read as an empty list and clear the
// organisation's allow-list.
func TestDecodeJSONRefusesUnknownFields(t *testing.T) {
	type body struct {
		Hosts []string `json:"hosts"`
	}
	for _, c := range []struct {
		name, in string
		ok       bool
	}{
		{"known field", `{"hosts":["a.example"]}`, true},
		{"misspelled field", `{"allowlist":["a.example"]}`, false},
		{"known and unknown", `{"hosts":[],"extra":1}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(c.in))
			var v body
			if got := decodeJSON(w, r, &v); got != c.ok {
				t.Fatalf("decodeJSON(%s) = %v, want %v (status %d, body %s)", c.in, got, c.ok, w.Code, w.Body)
			}
			if !c.ok {
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unknown field") {
					t.Fatalf("want 400 naming the unknown field, got %d %s", w.Code, w.Body)
				}
				if len(v.Hosts) != 0 {
					t.Fatalf("a refused body must not be applied: %+v", v)
				}
			}
		})
	}
}

func TestProvisionErrorUnreachableIs503(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).provisionError(rec, "demote", fmt.Errorf("%w: connection refused", provision.ErrUnreachable))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not reachable") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
