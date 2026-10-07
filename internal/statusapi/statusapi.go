// Package statusapi is the contract between pgdock-server and the separately
// hosted status service, pgdock-status (V3 §2.6): signed heartbeats for the
// components the status service can't probe from outside, and incidents
// the platform admin posts in PGDock. Both directions carry a
// PGDock-Signature over the body with the shared push secret.
package statusapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/signature"
)

// Paths on the status service.
const (
	PathHeartbeat = "/api/v1/heartbeat"
	// PathIncidents + id: PUT replaces one incident (and its updates).
	PathIncidents = "/api/v1/incidents/"
	// PathSLATargets: PUT replaces the endpoints the status service probes
	// for the SLA (V3 §2.7); PathSLAResults: POST returns its per-minute
	// results since a time (a POST so the request is signed like pushes).
	PathSLATargets = "/api/v1/sla/targets"
	PathSLAResults = "/api/v1/sla/results"
)

// SLATarget is a project's pooler endpoint to probe: a connection string
// with a login that may only connect and run SELECT 1.
type SLATarget struct {
	ID  string `json:"id"`
	DSN string `json:"dsn"`
}

// SLATargets is the full list (PUT replaces it).
type SLATargets struct {
	Targets []SLATarget `json:"targets"`
}

// SLAResultsRequest asks for results of minutes at or after Since.
type SLAResultsRequest struct {
	Since time.Time `json:"since"`
}

// SLAResult is one target's result for one minute: OK when every probe
// in the minute connected and ran its query.
type SLAResult struct {
	ID     string    `json:"id"`
	Minute time.Time `json:"minute"`
	OK     bool      `json:"ok"`
}

// SLAResults answers an SLAResultsRequest.
type SLAResults struct {
	Results []SLAResult `json:"results"`
}

// Validate checks targets.
func (t *SLATargets) Validate() error {
	if len(t.Targets) > 10000 {
		return errors.New("too many targets")
	}
	for _, x := range t.Targets {
		if !ValidID(x.ID) {
			return fmt.Errorf("target ID %q is not valid", x.ID)
		}
		if !strings.HasPrefix(x.DSN, "postgres://") && !strings.HasPrefix(x.DSN, "postgresql://") {
			return fmt.Errorf("target %s: not a postgres URL", x.ID)
		}
	}
	return nil
}

// Component states, best to worst.
const (
	Operational = "operational"
	Unknown     = "unknown"
	Degraded    = "degraded"
	Down        = "down"
)

// Rank orders states for "worst wins" (operational 0 … down 3).
func Rank(state string) int {
	switch state {
	case Operational:
		return 0
	case Degraded:
		return 2
	case Down:
		return 3
	default:
		return 1
	}
}

// StateOf is the inverse of Rank.
func StateOf(rank int) string {
	return [...]string{Operational, Unknown, Degraded, Down}[max(0, min(rank, 3))]
}

// Incident severities and statuses.
var (
	Severities = []string{"minor", "major", "critical", "maintenance"}
	Statuses   = []string{"investigating", "identified", "monitoring", "resolved"}
)

// Heartbeat reports components' states as pgdock-server sees them.
type Heartbeat struct {
	SentAt     time.Time        `json:"sent_at"`
	Components []ComponentState `json:"components"`
}

// ComponentState is one component's state in a heartbeat.
type ComponentState struct {
	ID     string `json:"id"`
	Status string `json:"status"` // operational, degraded or down
	Detail string `json:"detail,omitempty"`
}

// Incident is an incident as the status page shows it.
type Incident struct {
	ID         string           `json:"id"`
	Title      string           `json:"title"`
	Components []string         `json:"components"`
	Region     string           `json:"region,omitempty"`
	Severity   string           `json:"severity"`
	Status     string           `json:"status"`
	StartedAt  time.Time        `json:"started_at"`
	ResolvedAt *time.Time       `json:"resolved_at,omitempty"`
	Updates    []IncidentUpdate `json:"updates"`
	// Auto is set by the status service on incidents it opened itself.
	Auto bool `json:"auto,omitempty"`
}

// IncidentUpdate is one posted update. ID is unique within the incident.
type IncidentUpdate struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"`
	Body     string    `json:"body"`
	PostedAt time.Time `json:"posted_at"`
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// ValidID reports whether s is usable as a component, incident or update ID.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Validate checks a heartbeat.
func (h *Heartbeat) Validate() error {
	if len(h.Components) > 100 {
		return errors.New("too many components")
	}
	for _, c := range h.Components {
		if !ValidID(c.ID) {
			return fmt.Errorf("component ID %q is not valid", c.ID)
		}
		if c.Status != Operational && c.Status != Degraded && c.Status != Down {
			return fmt.Errorf("component %s: status %q is not operational, degraded or down", c.ID, c.Status)
		}
		if len(c.Detail) > 500 {
			return fmt.Errorf("component %s: detail is too long", c.ID)
		}
	}
	return nil
}

// Validate checks an incident and sorts its updates by time.
func (in *Incident) Validate() error {
	var errs []error
	if !ValidID(in.ID) {
		errs = append(errs, fmt.Errorf("incident ID %q is not valid", in.ID))
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 200 {
		errs = append(errs, errors.New("the title must be 1–200 characters"))
	}
	if len(in.Components) == 0 || len(in.Components) > 50 {
		errs = append(errs, errors.New("an incident affects 1–50 components"))
	}
	for _, c := range in.Components {
		if !ValidID(c) {
			errs = append(errs, fmt.Errorf("component ID %q is not valid", c))
		}
	}
	if !slices.Contains(Severities, in.Severity) {
		errs = append(errs, fmt.Errorf("severity %q is not one of %s", in.Severity, strings.Join(Severities, ", ")))
	}
	if !slices.Contains(Statuses, in.Status) {
		errs = append(errs, fmt.Errorf("status %q is not one of %s", in.Status, strings.Join(Statuses, ", ")))
	}
	if in.StartedAt.IsZero() {
		errs = append(errs, errors.New("started_at is required"))
	}
	if (in.Status == "resolved") != (in.ResolvedAt != nil) {
		errs = append(errs, errors.New("resolved_at is set exactly when the status is resolved"))
	}
	if len(in.Updates) > 500 {
		errs = append(errs, errors.New("too many updates"))
	}
	seen := map[string]bool{}
	for _, u := range in.Updates {
		if !ValidID(u.ID) || seen[u.ID] {
			errs = append(errs, fmt.Errorf("update ID %q is not valid or repeated", u.ID))
		}
		seen[u.ID] = true
		if !slices.Contains(Statuses, u.Status) {
			errs = append(errs, fmt.Errorf("update %s: status %q is not valid", u.ID, u.Status))
		}
		if strings.TrimSpace(u.Body) == "" || len(u.Body) > 10000 {
			errs = append(errs, fmt.Errorf("update %s: the body must be 1–10000 characters", u.ID))
		}
		if u.PostedAt.IsZero() {
			errs = append(errs, fmt.Errorf("update %s: posted_at is required", u.ID))
		}
	}
	slices.SortStableFunc(in.Updates, func(a, b IncidentUpdate) int { return a.PostedAt.Compare(b.PostedAt) })
	return errors.Join(errs...)
}

// Client pushes to a status service.
type Client struct {
	URL    string // base URL, e.g. https://status.example.com
	Secret string
	HTTP   *http.Client
}

// Heartbeat sends h.
func (c *Client) Heartbeat(ctx context.Context, h Heartbeat) error {
	return c.send(ctx, http.MethodPost, PathHeartbeat, h)
}

// PutSLATargets replaces the SLA probe targets.
func (c *Client) PutSLATargets(ctx context.Context, t SLATargets) error {
	return c.send(ctx, http.MethodPut, PathSLATargets, t)
}

// SLAResults fetches results since a time.
func (c *Client) SLAResults(ctx context.Context, since time.Time) (SLAResults, error) {
	var out SLAResults
	return out, c.call(ctx, http.MethodPost, PathSLAResults, SLAResultsRequest{Since: since}, &out)
}

// PutIncident creates or replaces in.
func (c *Client) PutIncident(ctx context.Context, in Incident) error {
	return c.send(ctx, http.MethodPut, PathIncidents+in.ID, in)
}

func (c *Client) send(ctx context.Context, method, path string, v any) error {
	return c.call(ctx, method, path, v, nil)
}

func (c *Client) call(ctx context.Context, method, path string, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(signature.Header, signature.Sign(c.Secret, time.Now(), body))
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status service: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
	}
	return nil
}
