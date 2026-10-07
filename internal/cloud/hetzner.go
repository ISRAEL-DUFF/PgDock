package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultHetznerAPI is the Hetzner Cloud API.
const DefaultHetznerAPI = "https://api.hetzner.cloud/v1"

// HetznerProvider creates servers in a Hetzner Cloud project.
type HetznerProvider struct {
	// API is the base URL (DefaultHetznerAPI, or a fake in tests).
	API string
	// Token is a Hetzner Cloud API token with read and write access.
	Token string
	HTTP  *http.Client
}

// Name is "hetzner".
func (*HetznerProvider) Name() string { return Hetzner }

func (h *HetznerProvider) client() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (h *HetznerProvider) base() string {
	if h.API == "" {
		return DefaultHetznerAPI
	}
	return strings.TrimRight(h.API, "/")
}

func (h *HetznerProvider) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return fmt.Errorf("hetzner %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error.Message != "" {
			return fmt.Errorf("hetzner %s %s: %s (%s)", method, path, e.Error.Message, e.Error.Code)
		}
		return fmt.Errorf("hetzner %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// hServer is a server as the Hetzner API returns it.
type hServer struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	Labels     map[string]string `json:"labels"`
	ServerType struct {
		Name string `json:"name"`
	} `json:"server_type"`
	Datacenter struct {
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
	} `json:"datacenter"`
	PublicNet struct {
		IPv4 *struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
	PrivateNet []struct {
		IP string `json:"ip"`
	} `json:"private_net"`
}

func (s hServer) server() Server {
	out := Server{ID: strconv.FormatInt(s.ID, 10), Name: s.Name, Type: s.ServerType.Name, Location: s.Datacenter.Location.Name, Status: s.Status, Labels: s.Labels}
	if s.PublicNet.IPv4 != nil {
		out.PublicIPv4 = s.PublicNet.IPv4.IP
	}
	if len(s.PrivateNet) > 0 {
		out.PrivateIP = s.PrivateNet[0].IP
	}
	return out
}

func id64(kind, id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("hetzner %s ID %q: %w", kind, id, err)
	}
	return n, nil
}

// CreateServer creates and starts a server with spec's cloud-init.
func (h *HetznerProvider) CreateServer(ctx context.Context, spec ServerSpec) (Server, error) {
	body := map[string]any{
		"name": spec.Name, "server_type": spec.Type, "image": spec.Image, "user_data": spec.UserData,
		"labels": spec.Labels, "start_after_create": true,
	}
	if spec.Location != "" {
		body["location"] = spec.Location
	}
	if spec.Network != "" {
		n, err := id64("network", spec.Network)
		if err != nil {
			return Server{}, err
		}
		body["networks"] = []int64{n}
	}
	if spec.PlacementGroup != "" {
		n, err := id64("placement group", spec.PlacementGroup)
		if err != nil {
			return Server{}, err
		}
		body["placement_group"] = n
	}
	if len(spec.SSHKeys) > 0 {
		body["ssh_keys"] = spec.SSHKeys
	}
	var out struct {
		Server hServer `json:"server"`
	}
	if err := h.do(ctx, http.MethodPost, "/servers", body, &out); err != nil {
		return Server{}, err
	}
	return out.Server.server(), nil
}

type hPlacementGroup struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Servers []int64 `json:"servers"`
}

func (g hPlacementGroup) group() PlacementGroup {
	return PlacementGroup{ID: strconv.FormatInt(g.ID, 10), Name: g.Name, Servers: len(g.Servers)}
}

// EnsurePlacementGroup finds the spread group called name, or creates it.
func (h *HetznerProvider) EnsurePlacementGroup(ctx context.Context, name string, labels map[string]string) (PlacementGroup, error) {
	var list struct {
		PlacementGroups []hPlacementGroup `json:"placement_groups"`
	}
	if err := h.do(ctx, http.MethodGet, "/placement_groups?name="+url.QueryEscape(name), nil, &list); err != nil {
		return PlacementGroup{}, err
	}
	for _, g := range list.PlacementGroups {
		if g.Name == name {
			if g.Type != "spread" {
				return PlacementGroup{}, fmt.Errorf("hetzner placement group %s is %s, not spread", name, g.Type)
			}
			return g.group(), nil
		}
	}
	var out struct {
		PlacementGroup hPlacementGroup `json:"placement_group"`
	}
	if err := h.do(ctx, http.MethodPost, "/placement_groups", map[string]any{"name": name, "type": "spread", "labels": labels}, &out); err != nil {
		return PlacementGroup{}, err
	}
	return out.PlacementGroup.group(), nil
}

// DeleteServer deletes a server; one already gone is not an error.
func (h *HetznerProvider) DeleteServer(ctx context.Context, id string) error {
	if _, err := id64("server", id); err != nil {
		return err
	}
	err := h.do(ctx, http.MethodDelete, "/servers/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ListServers lists the project's servers carrying f's labels.
func (h *HetznerProvider) ListServers(ctx context.Context, f Filter) ([]Server, error) {
	var sel []string
	for k, v := range f.Labels {
		sel = append(sel, k+"="+v)
	}
	sort.Strings(sel)
	var out []Server
	for page := 1; page > 0; {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"50"}}
		if len(sel) > 0 {
			q.Set("label_selector", strings.Join(sel, ","))
		}
		var resp struct {
			Servers []hServer `json:"servers"`
			Meta    struct {
				Pagination struct {
					NextPage *int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		if err := h.do(ctx, http.MethodGet, "/servers?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		for _, s := range resp.Servers {
			out = append(out, s.server())
		}
		page = 0
		if resp.Meta.Pagination.NextPage != nil {
			page = *resp.Meta.Pagination.NextPage
		}
	}
	return out, nil
}

// CreateVolume creates an ext4 volume.
func (h *HetznerProvider) CreateVolume(ctx context.Context, spec VolumeSpec) (Volume, error) {
	var out struct {
		Volume struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Size int    `json:"size"`
		} `json:"volume"`
	}
	if err := h.do(ctx, http.MethodPost, "/volumes", map[string]any{
		"name": spec.Name, "size": spec.SizeGB, "location": spec.Location, "labels": spec.Labels, "format": "ext4",
	}, &out); err != nil {
		return Volume{}, err
	}
	return Volume{ID: strconv.FormatInt(out.Volume.ID, 10), Name: out.Volume.Name, SizeGB: out.Volume.Size}, nil
}

// AttachVolume attaches and mounts a volume on a server.
func (h *HetznerProvider) AttachVolume(ctx context.Context, volumeID, serverID string) error {
	s, err := id64("server", serverID)
	if err != nil {
		return err
	}
	if _, err := id64("volume", volumeID); err != nil {
		return err
	}
	return h.do(ctx, http.MethodPost, "/volumes/"+volumeID+"/actions/attach", map[string]any{"server": s, "automount": true}, nil)
}

// AssignFloatingIP routes a floating IP to a server.
func (h *HetznerProvider) AssignFloatingIP(ctx context.Context, ipID, serverID string) error {
	s, err := id64("server", serverID)
	if err != nil {
		return err
	}
	if _, err := id64("floating IP", ipID); err != nil {
		return err
	}
	return h.do(ctx, http.MethodPost, "/floating_ips/"+ipID+"/actions/assign", map[string]int64{"server": s}, nil)
}

// PriceCatalog lists every server type that isn't deprecated, in each
// location, at its net monthly price.
func (h *HetznerProvider) PriceCatalog(ctx context.Context) ([]ServerPrice, error) {
	var resp struct {
		ServerTypes []struct {
			Name        string  `json:"name"`
			Cores       int     `json:"cores"`
			Memory      float64 `json:"memory"`
			Disk        int     `json:"disk"`
			Deprecation any     `json:"deprecation"`
			Prices      []struct {
				Location     string `json:"location"`
				PriceMonthly struct {
					Net string `json:"net"`
				} `json:"price_monthly"`
			} `json:"prices"`
		} `json:"server_types"`
	}
	if err := h.do(ctx, http.MethodGet, "/server_types?per_page=50", nil, &resp); err != nil {
		return nil, err
	}
	var out []ServerPrice
	for _, t := range resp.ServerTypes {
		if t.Deprecation != nil {
			continue
		}
		for _, p := range t.Prices {
			eur, err := strconv.ParseFloat(p.PriceMonthly.Net, 64)
			if err != nil {
				continue
			}
			out = append(out, ServerPrice{
				Type: t.Name, Location: p.Location, CPUs: t.Cores, MemoryGB: t.Memory, DiskGB: t.Disk,
				MonthlyMinor: int64(math.Round(eur * 100)), Currency: "EUR",
			})
		}
	}
	return out, nil
}
