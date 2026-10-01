package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/israel-duff/pgdock/internal/alerts"
	"github.com/israel-duff/pgdock/internal/api/gen"
)

func (s *Server) requireAlerts(w http.ResponseWriter) bool {
	if s.alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "alerts are not configured")
		return false
	}
	return true
}

// ListAlerts implements GET /api/v1/alerts.
func (s *Server) ListAlerts(w http.ResponseWriter, r *http.Request, params gen.ListAlertsParams) {
	if !s.requireAlerts(w) {
		return
	}
	limit := 100
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 500 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 500")
			return
		}
		limit = *params.Limit
	}
	var status *string
	if params.Status != nil {
		st := string(*params.Status)
		status = &st
	}
	list, err := s.alerts.List(r.Context(), status, limit)
	if err != nil {
		s.internalError(w, "list alerts", err)
		return
	}
	total, critical, err := s.alerts.Firing(r.Context())
	if err != nil {
		s.internalError(w, "list alerts", err)
		return
	}
	out := gen.AlertList{Items: make([]gen.Alert, 0, len(list)), Firing: total, Critical: critical}
	for _, a := range list {
		detail := map[string]any{}
		_ = json.Unmarshal(a.Detail, &detail)
		out.Items = append(out.Items, gen.Alert{
			Id: a.ID, Kind: a.Kind, Severity: gen.AlertSeverity(a.Severity), Status: gen.AlertStatus(a.Status), Summary: a.Summary,
			TargetType: a.TargetType, TargetId: a.TargetID, TargetName: a.TargetName, StartedAt: a.StartedAt, LastSeenAt: a.LastSeenAt,
			ResolvedAt: a.ResolvedAt, NotifiedAt: a.NotifiedAt, DeliveryError: a.DeliveryError, Detail: detail,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func toAPIAlertSettings(st alerts.Settings) gen.AlertSettings {
	out := gen.AlertSettings{WebhookUrl: st.WebhookURL, HasWebhookSecret: st.HasWebhookSecret}
	if m := st.SMTP; m != nil {
		out.Smtp = &gen.AlertSmtp{Host: m.Host, Port: m.Port, HasPassword: m.HasPassword, From: m.From, To: m.To, Tls: gen.AlertSmtpTls(m.TLS)}
		if m.Username != "" {
			out.Smtp.Username = &m.Username
		}
	}
	return out
}

// GetAlertSettings implements GET /api/v1/settings/alerts.
func (s *Server) GetAlertSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	st, err := s.alerts.Settings(r.Context())
	if err != nil {
		s.internalError(w, "alert settings", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIAlertSettings(st))
}

// PutAlertSettings implements PUT /api/v1/settings/alerts.
func (s *Server) PutAlertSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	var req gen.AlertSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := alerts.Update{WebhookSecret: req.WebhookSecret}
	if req.WebhookUrl != nil {
		u.WebhookURL = *req.WebhookUrl
	}
	a := auditFrom(r.Context())
	a.set("webhook", u.WebhookURL != "")
	if m := req.Smtp; m != nil {
		su := &alerts.SMTPUpdate{Host: m.Host, From: m.From, To: m.To, Password: m.Password}
		if m.Port != nil {
			su.Port = *m.Port
		}
		if m.Username != nil {
			su.Username = *m.Username
		}
		if m.Tls != nil {
			su.TLS = string(*m.Tls)
		}
		u.SMTP = su
	}
	a.set("email", u.SMTP != nil)
	st, err := s.alerts.Save(r.Context(), u)
	if errors.Is(err, alerts.ErrInvalid) {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "save alert settings", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIAlertSettings(st))
}

// TestAlertSettings implements POST /api/v1/settings/alerts/test.
func (s *Server) TestAlertSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	res, err := s.alerts.SendTest(r.Context())
	if err != nil {
		s.internalError(w, "test alerts", err)
		return
	}
	out := gen.AlertTestResult{}
	for _, c := range res {
		item := struct {
			Channel string  `json:"channel"`
			Error   *string `json:"error,omitempty"`
			Ok      bool    `json:"ok"`
		}{Channel: c.Channel, Ok: c.OK}
		if c.Error != "" {
			e := c.Error
			item.Error = &e
		}
		out.Results = append(out.Results, item)
	}
	if out.Results == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "no alert channel is configured")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
