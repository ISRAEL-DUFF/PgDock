// Package cdn purges cached URLs from the CDN in front of pgdock-edge
// (V4 §5.4): a public bucket made private leaves the cache at once instead
// of after its max-age.
package cdn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Cloudflare purges by URL prefix through Cloudflare's API.
type Cloudflare struct {
	ZoneID string
	Token  string
	// BaseURL is the API's (tests' fake); empty is Cloudflare's.
	BaseURL string
	HTTP    *http.Client
}

// purgeBatch is how many prefixes Cloudflare takes per request.
const purgeBatch = 30

// PurgePrefixes removes every cached URL under prefixes (host/path, no
// scheme).
func (c Cloudflare) PurgePrefixes(ctx context.Context, prefixes []string) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	for len(prefixes) > 0 {
		n := min(len(prefixes), purgeBatch)
		body, _ := json.Marshal(map[string][]string{"prefixes": prefixes[:n]})
		prefixes = prefixes[n:]
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/zones/"+c.ZoneID+"/purge_cache", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			return fmt.Errorf("cloudflare purge: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		_ = res.Body.Close()
		var out struct {
			Success bool `json:"success"`
		}
		if res.StatusCode/100 != 2 || json.Unmarshal(raw, &out) != nil || !out.Success {
			return fmt.Errorf("cloudflare purge: %d %s", res.StatusCode, bytes.TrimSpace(raw))
		}
	}
	return nil
}
