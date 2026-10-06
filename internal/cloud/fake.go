package cloud

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FakeHetzner is an in-memory stand-in for the parts of the Hetzner Cloud
// API HetznerProvider uses: servers, volumes, floating IP assignment and
// server types. Tests and the development environment run it in place of
// the real API; a test boots a "server" itself, from the cloud-init its
// create carried (Created).
type FakeHetzner struct {
	Token string

	mu      sync.Mutex
	next    int64
	servers map[int64]*FakeServer
	volumes map[int64]int64 // volume → attached server (0: none)
	ips     map[string]int64
	types   []fakeType
}

// FakeServer is a server the fake holds.
type FakeServer struct {
	ID       int64
	Name     string
	Type     string
	Location string
	Labels   map[string]string
	UserData string
	Deleted  bool
}

type fakeType struct {
	Name    string
	Cores   int
	Memory  float64
	Disk    int
	Monthly map[string]string // location → net EUR
	Retired bool
}

// NewFakeHetzner returns a fake with a few of Hetzner's shared-vCPU types
// in fsn1 and nbg1, at prices close to the real ones.
func NewFakeHetzner(token string) *FakeHetzner {
	f := &FakeHetzner{Token: token, next: 1000, servers: map[int64]*FakeServer{}, volumes: map[int64]int64{}, ips: map[string]int64{}}
	for _, t := range []struct {
		name  string
		cores int
		mem   float64
		disk  int
		eur   string
	}{{"cpx11", 2, 2, 40, "4.5100"}, {"cpx21", 3, 4, 80, "8.2100"}, {"cpx31", 4, 8, 160, "15.1100"}, {"cpx41", 8, 16, 240, "27.9900"}, {"cpx51", 16, 32, 360, "56.4900"}} {
		f.types = append(f.types, fakeType{Name: t.name, Cores: t.cores, Memory: t.mem, Disk: t.disk, Monthly: map[string]string{"fsn1": t.eur, "nbg1": t.eur}})
	}
	f.types = append(f.types, fakeType{Name: "cx11", Cores: 1, Memory: 2, Disk: 20, Monthly: map[string]string{"fsn1": "3.2900"}, Retired: true})
	return f
}

// Created returns every server created, in order (deleted ones too).
func (f *FakeHetzner) Created() []FakeServer {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []FakeServer
	for _, s := range f.servers {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Live returns the servers not deleted.
func (f *FakeHetzner) Live() []FakeServer {
	var out []FakeServer
	for _, s := range f.Created() {
		if !s.Deleted {
			out = append(out, s)
		}
	}
	return out
}

func (f *FakeHetzner) json(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *FakeHetzner) fail(w http.ResponseWriter, code int, c, msg string) {
	f.json(w, code, map[string]any{"error": map[string]string{"code": c, "message": msg}})
}

func (s *FakeServer) view() map[string]any {
	return map[string]any{
		"id": s.ID, "name": s.Name, "status": "running", "labels": s.Labels,
		"server_type": map[string]string{"name": s.Type},
		"datacenter":  map[string]any{"location": map[string]string{"name": s.Location}},
		"public_net":  map[string]any{"ipv4": map[string]string{"ip": "203.0.113." + strconv.FormatInt(s.ID%250, 10)}},
		"private_net": []map[string]string{{"ip": "10.0.0." + strconv.FormatInt(s.ID%250, 10)}},
	}
}

// ServeHTTP implements the API under any prefix (e.g. /v1).
func (f *FakeHetzner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.Token {
		f.fail(w, http.StatusUnauthorized, "unauthorized", "unable to authenticate")
		return
	}
	path := r.URL.Path
	for _, root := range []string{"/servers", "/volumes", "/floating_ips", "/server_types"} {
		if i := strings.Index(path, root); i >= 0 {
			path = path[i:]
			break
		}
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case parts[0] == "server_types" && r.Method == http.MethodGet:
		var out []map[string]any
		for _, t := range f.types {
			var prices []map[string]any
			for loc, eur := range t.Monthly {
				prices = append(prices, map[string]any{"location": loc, "price_monthly": map[string]string{"net": eur, "gross": eur}})
			}
			var dep any
			if t.Retired {
				dep = map[string]string{"announced": "2024-01-01T00:00:00Z"}
			}
			out = append(out, map[string]any{"name": t.Name, "cores": t.Cores, "memory": t.Memory, "disk": t.Disk, "deprecation": dep, "prices": prices})
		}
		f.json(w, http.StatusOK, map[string]any{"server_types": out})
	case parts[0] == "servers" && len(parts) == 1 && r.Method == http.MethodPost:
		var in struct {
			Name       string            `json:"name"`
			ServerType string            `json:"server_type"`
			Location   string            `json:"location"`
			UserData   string            `json:"user_data"`
			Labels     map[string]string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" || in.ServerType == "" {
			f.fail(w, http.StatusBadRequest, "invalid_input", "name and server_type are required")
			return
		}
		known := false
		for _, t := range f.types {
			known = known || (t.Name == in.ServerType && !t.Retired)
		}
		if !known {
			f.fail(w, http.StatusBadRequest, "invalid_input", "unknown server type "+in.ServerType)
			return
		}
		for _, s := range f.servers {
			if s.Name == in.Name && !s.Deleted {
				f.fail(w, http.StatusConflict, "uniqueness_error", "server name is already used")
				return
			}
		}
		f.next++
		s := &FakeServer{ID: f.next, Name: in.Name, Type: in.ServerType, Location: in.Location, Labels: in.Labels, UserData: in.UserData}
		if s.Location == "" {
			s.Location = "fsn1"
		}
		f.servers[s.ID] = s
		f.json(w, http.StatusCreated, map[string]any{"server": s.view(), "action": map[string]any{"id": f.next, "status": "running"}})
	case parts[0] == "servers" && len(parts) == 1 && r.Method == http.MethodGet:
		want := map[string]string{}
		for _, kv := range strings.Split(r.URL.Query().Get("label_selector"), ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				want[k] = v
			}
		}
		var out []map[string]any
		var ids []int64
		for id := range f.servers {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			s := f.servers[id]
			match := !s.Deleted
			for k, v := range want {
				match = match && s.Labels[k] == v
			}
			if match {
				out = append(out, s.view())
			}
		}
		f.json(w, http.StatusOK, map[string]any{"servers": out, "meta": map[string]any{"pagination": map[string]any{"next_page": nil}}})
	case parts[0] == "servers" && len(parts) == 2 && r.Method == http.MethodDelete:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		s, ok := f.servers[id]
		if !ok || s.Deleted {
			f.fail(w, http.StatusNotFound, "not_found", "server not found")
			return
		}
		s.Deleted = true
		f.json(w, http.StatusOK, map[string]any{"action": map[string]any{"status": "running"}})
	case parts[0] == "volumes" && len(parts) == 1 && r.Method == http.MethodPost:
		var in struct {
			Name string `json:"name"`
			Size int    `json:"size"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.next++
		f.volumes[f.next] = 0
		f.json(w, http.StatusCreated, map[string]any{"volume": map[string]any{"id": f.next, "name": in.Name, "size": in.Size}})
	case parts[0] == "volumes" && len(parts) == 4 && parts[3] == "attach" && r.Method == http.MethodPost:
		vid, _ := strconv.ParseInt(parts[1], 10, 64)
		var in struct {
			Server int64 `json:"server"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, ok := f.volumes[vid]; !ok || f.servers[in.Server] == nil {
			f.fail(w, http.StatusNotFound, "not_found", "volume or server not found")
			return
		}
		f.volumes[vid] = in.Server
		f.json(w, http.StatusCreated, map[string]any{"action": map[string]any{"status": "running"}})
	case parts[0] == "floating_ips" && len(parts) == 4 && parts[3] == "assign" && r.Method == http.MethodPost:
		var in struct {
			Server int64 `json:"server"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if f.servers[in.Server] == nil {
			f.fail(w, http.StatusNotFound, "not_found", "server not found")
			return
		}
		f.ips[parts[1]] = in.Server
		f.json(w, http.StatusCreated, map[string]any{"action": map[string]any{"status": "running"}})
	default:
		f.fail(w, http.StatusNotFound, "not_found", "no such endpoint in the fake")
	}
}
