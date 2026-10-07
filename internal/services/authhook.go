package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/outbound"
	"github.com/israel-duff/pgdock/internal/store"
)

// Auth webhook events (V4 §4.7).
const (
	HookBeforeSignup = "before_signup"
	HookAfterSignup  = "after_signup"
	HookAfterSignin  = "after_signin"

	hookBatch    = 50
	hookMaxTries = 8
	// beforeSignupTimeout bounds the synchronous before-sign-up call; a
	// hook that doesn't answer in time lets the sign-up through (fail-open,
	// documented) so a broken hook doesn't lock out every new user.
	beforeSignupTimeout = 3 * time.Second
	hookKeep            = 14 * 24 * time.Hour
)

// AuthHook takes an event from pgdock-edge: after-sign-up and after-sign-in
// events are queued and delivered with retries; before-sign-up (Wait) is
// asked now and its answer returned.
func (s *Service) AuthHook(ctx context.Context, h edgeapi.AuthHook) (edgeapi.HookDecision, error) {
	var none edgeapi.HookDecision
	svc, err := store.New(s.db).ProjectServicesByRef(ctx, h.Ref)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !svc.Enabled) {
		return none, ErrNotFound
	}
	if err != nil {
		return none, err
	}
	st, _, prov, err := s.loadAuth(ctx, svc.ProjectID)
	if err != nil {
		return none, err
	}
	var hookURL string
	switch h.Event {
	case HookBeforeSignup:
		hookURL = strOr(st.BeforeSignupURL, "")
	case HookAfterSignup:
		hookURL = strOr(st.AfterSignupURL, "")
	case HookAfterSignin:
		hookURL = strOr(st.AfterSigninURL, "")
	default:
		return none, fmt.Errorf("%w: no hook event %q", ErrInvalid, h.Event)
	}
	if hookURL == "" {
		return none, nil
	}
	if h.Wait != (h.Event == HookBeforeSignup) {
		return none, fmt.Errorf("%w: only before_signup is answered", ErrInvalid)
	}
	payload, _ := json.Marshal(h.Payload)
	if !h.Wait {
		err := store.New(s.db).InsertAuthHook(ctx, store.InsertAuthHookParams{ID: uuid.New(), ProjectID: svc.ProjectID,
			Event: h.Event, Payload: payload})
		if err == nil {
			s.kickEmail()
		}
		return none, err
	}
	if s.Outbound == nil {
		return none, nil
	}
	p, err := store.New(s.db).GetProject(ctx, svc.ProjectID)
	if err != nil {
		return none, err
	}
	body := hookBody(h.Event, svc.ProjectID, uuid.New(), payload)
	resp, err := s.Outbound.Do(ctx, outbound.Request{OrgID: p.OrgID, URL: hookURL, Body: body, Secret: prov.HookSecret,
		Timeout: beforeSignupTimeout})
	if err != nil {
		s.log.Warn("before-sign-up hook", "project", svc.ProjectID, "err", err)
		return none, nil
	}
	return decision(resp), nil
}

// decision reads a before-sign-up answer: a 2xx lets the sign-up through
// unless its body says {"decision":"reject"}; a 4xx rejects it (its body's
// "message" shown to the user); anything else lets it through.
func decision(resp outbound.Response) edgeapi.HookDecision {
	var out struct {
		Decision string `json:"decision"`
		Message  string `json:"message"`
	}
	_ = json.Unmarshal([]byte(resp.Snippet), &out)
	msg := strings.TrimSpace(out.Message)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	switch resp.StatusCode / 100 {
	case 2:
		if strings.EqualFold(out.Decision, "reject") {
			return edgeapi.HookDecision{Reject: true, Message: msg}
		}
	case 4:
		return edgeapi.HookDecision{Reject: true, Message: msg}
	}
	return edgeapi.HookDecision{}
}

func hookBody(event string, projectID, id uuid.UUID, payload []byte) []byte {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	b, _ := json.Marshal(map[string]any{"type": event, "id": id, "project_id": projectID,
		"created_at": time.Now().UTC(), "data": json.RawMessage(payload)})
	return b
}

func (s *Service) kickEmail() {
	select {
	case s.emailKick <- struct{}{}:
	default:
	}
}

func (s *Service) sendAuthHooks(ctx context.Context) error {
	if s.Outbound == nil {
		return nil
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		due, err := q.DueAuthHooks(ctx, hookBatch)
		if err != nil {
			return err
		}
		for _, d := range due {
			status, err := s.deliverHook(ctx, d)
			p := store.MarkAuthHookParams{ID: d.ID, LastStatus: status, Delivered: err == nil, NextAttemptAt: d.NextAttemptAt}
			if err != nil {
				msg := err.Error()
				p.LastError = &msg
				p.Failed = d.Attempts+1 >= hookMaxTries
				p.NextAttemptAt = time.Now().Add(time.Duration(1<<min(d.Attempts, 8)) * 15 * time.Second)
			}
			if err := q.MarkAuthHook(ctx, p); err != nil {
				return err
			}
		}
		_, err = q.PruneAuthHooks(ctx, time.Now().Add(-hookKeep))
		return err
	})
}

func (s *Service) deliverHook(ctx context.Context, d store.AuthHookOutbox) (*int32, error) {
	st, _, prov, err := s.loadAuth(ctx, d.ProjectID)
	if err != nil {
		return nil, err
	}
	hookURL := strOr(st.AfterSignupURL, "")
	if d.Event == HookAfterSignin {
		hookURL = strOr(st.AfterSigninURL, "")
	}
	if hookURL == "" {
		return nil, nil // turned off since; drop it
	}
	p, err := store.New(s.db).GetProject(ctx, d.ProjectID)
	if err != nil {
		return nil, err
	}
	resp, err := s.Outbound.Do(ctx, outbound.Request{OrgID: p.OrgID, URL: hookURL, Body: hookBody(d.Event, d.ProjectID, d.ID, d.Payload),
		Secret: prov.HookSecret, Timeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	code := int32(resp.StatusCode)
	if resp.StatusCode/100 != 2 {
		return &code, fmt.Errorf("the hook answered %d", resp.StatusCode)
	}
	return &code, nil
}

// AuthHooks lists a project's recent hook deliveries.
func (s *Service) AuthHooks(ctx context.Context, projectID uuid.UUID) ([]store.ProjectAuthHooksRow, error) {
	return store.New(s.db).ProjectAuthHooks(ctx, projectID)
}
