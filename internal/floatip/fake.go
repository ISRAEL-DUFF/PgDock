package floatip

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Fake is an in-memory stand-in for the parts of the Hetzner Cloud API the
// Hetzner provider uses: one floating IP and its assignment. Tests and the
// Docker HA environment run it in place of the real API.
type Fake struct {
	Token string
	IPID  string

	mu      sync.Mutex
	server  *int64
	assigns []int64
}

// Assigned returns the server the IP routes to (0 when unassigned).
func (f *Fake) Assigned() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.server == nil {
		return 0
	}
	return *f.server
}

// Assignments returns every server the IP was assigned to, in order.
func (f *Fake) Assignments() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.assigns...)
}

// ServeHTTP implements GET /floating_ips/{id} and
// POST /floating_ips/{id}/actions/assign, under any prefix (e.g. /v1).
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.Token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"unable to authenticate"}}`))
		return
	}
	path := r.URL.Path
	i := strings.Index(path, "/floating_ips/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	rest := strings.Split(strings.Trim(path[i+len("/floating_ips/"):], "/"), "/")
	if rest[0] != f.IPID {
		http.NotFound(w, r)
		return
	}
	id, _ := strconv.ParseInt(f.IPID, 10, 64)
	switch {
	case len(rest) == 1 && r.Method == http.MethodGet:
		f.mu.Lock()
		var out floatingIP
		out.FloatingIP.ID, out.FloatingIP.IP, out.FloatingIP.Server = id, "203.0.113.10", f.server
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case len(rest) == 3 && rest[1] == "actions" && rest[2] == "assign" && r.Method == http.MethodPost:
		var in struct {
			Server int64 `json:"server"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Server == 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_input","message":"server is required"}}`))
			return
		}
		f.mu.Lock()
		s := in.Server
		f.server = &s
		f.assigns = append(f.assigns, s)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"action":{"id":1,"command":"assign_floating_ip","status":"running"}}`))
	default:
		http.NotFound(w, r)
	}
}
