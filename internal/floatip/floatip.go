// Package floatip assigns the edge pooler's floating IP to a server
// (V3 §2.1). keepalived on the pooler hosts decides which host should hold
// it and the agent there claims it; pgdock-server checks the assignment
// and corrects it when keepalived is split.
package floatip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Provider reads and changes which server a floating IP routes to.
type Provider interface {
	// Holder returns the provider's ID of the server the IP is assigned
	// to, or "" when it is unassigned.
	Holder(ctx context.Context) (string, error)
	// Assign routes the IP to server. Assigning to the current holder is a
	// no-op.
	Assign(ctx context.Context, server string) error
}

// None is the provider when there is no floating IP: a single pooler host,
// or one whose address keepalived moves on a shared network by itself.
type None struct{}

// Holder reports no assignment.
func (None) Holder(context.Context) (string, error) { return "", nil }

// Assign does nothing.
func (None) Assign(context.Context, string) error { return nil }

// DefaultHetznerAPI is the Hetzner Cloud API.
const DefaultHetznerAPI = "https://api.hetzner.cloud/v1"

// Hetzner assigns a Hetzner Cloud floating IP.
type Hetzner struct {
	// API is the base URL (DefaultHetznerAPI, or a fake in tests).
	API string
	// Token is a Hetzner Cloud API token with read and write access.
	Token string
	// IPID is the floating IP's numeric ID.
	IPID string
	HTTP *http.Client
}

// ErrNotFound means the floating IP does not exist in the project the
// token belongs to.
var ErrNotFound = errors.New("floating IP not found")

func (h *Hetzner) client() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (h *Hetzner) base() string {
	if h.API == "" {
		return DefaultHetznerAPI
	}
	return strings.TrimRight(h.API, "/")
}

func (h *Hetzner) do(ctx context.Context, method, path string, in, out any) error {
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

type floatingIP struct {
	FloatingIP struct {
		ID     int64  `json:"id"`
		IP     string `json:"ip"`
		Server *int64 `json:"server"`
	} `json:"floating_ip"`
}

// Holder returns the ID of the server the floating IP routes to.
func (h *Hetzner) Holder(ctx context.Context) (string, error) {
	var f floatingIP
	if err := h.do(ctx, http.MethodGet, "/floating_ips/"+h.IPID, nil, &f); err != nil {
		return "", err
	}
	if f.FloatingIP.Server == nil {
		return "", nil
	}
	return strconv.FormatInt(*f.FloatingIP.Server, 10), nil
}

// Assign routes the floating IP to server. Hetzner applies the change
// asynchronously; it usually takes effect within a couple of seconds.
func (h *Hetzner) Assign(ctx context.Context, server string) error {
	id, err := strconv.ParseInt(server, 10, 64)
	if err != nil {
		return fmt.Errorf("hetzner server ID %q: %w", server, err)
	}
	if cur, err := h.Holder(ctx); err == nil && cur == server {
		return nil
	}
	return h.do(ctx, http.MethodPost, "/floating_ips/"+h.IPID+"/actions/assign", map[string]int64{"server": id}, nil)
}

// hetznerMetadata is Hetzner Cloud's metadata service, reachable only from
// the server itself.
const hetznerMetadata = "http://169.254.169.254/hetzner/v1/metadata/instance-id"

// HetznerServerID asks Hetzner's metadata service for this server's ID.
func HetznerServerID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hetznerMetadata, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	id := strings.TrimSpace(string(b))
	if resp.StatusCode != http.StatusOK || id == "" {
		return "", fmt.Errorf("metadata service: HTTP %d", resp.StatusCode)
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return "", fmt.Errorf("metadata service returned %q", id)
	}
	return id, nil
}
