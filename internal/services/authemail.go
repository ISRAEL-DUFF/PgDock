package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// Auth email (V4 §4.5): pgdock-edge asks, pgdock-server renders the
// project's template and sends it from a queue, through the project's own
// SMTP or, at PlatformEmailsPerHour, the platform's. SMTP credentials stay
// here.

// ErrRateLimited is the project's email allowance used up.
var ErrRateLimited = errors.New("rate limited")

const (
	emailBatch      = 20
	emailMaxTries   = 5
	emailSendTimout = 30 * time.Second
)

type queuedEmail struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func emailAAD(id uuid.UUID) []byte { return []byte("auth_email_outbox.message_enc:" + id.String()) }

// QueueAuthEmail renders and queues an email pgdock-edge asked for.
func (s *Service) QueueAuthEmail(ctx context.Context, m edgeapi.AuthEmail) error {
	if _, ok := DefaultTemplates[m.Kind]; !ok {
		return fmt.Errorf("%w: no email kind %q", ErrInvalid, m.Kind)
	}
	if m.To == "" || len(m.To) > 254 {
		return fmt.Errorf("%w: a recipient is required", ErrInvalid)
	}
	svc, err := store.New(s.db).ProjectServicesByRef(ctx, m.Ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !svc.Enabled {
		return ErrNotFound
	}
	st, tpl, prov, err := s.loadAuth(ctx, svc.ProjectID)
	if err != nil {
		return err
	}
	site := st.Resolve().SiteURL
	if site == "" {
		site = s.URL(svc.Ref, "")
	}
	subject, body, err := Render(m.Kind, tpl[m.Kind], TemplateVars{Code: m.Code, Link: m.Link, Email: m.To, SiteURL: site})
	if err != nil {
		// A template that saved but fails on real data: fall back to the
		// default rather than leave the user without their code.
		if subject, body, err = Render(m.Kind, Template{}, TemplateVars{Code: m.Code, Link: m.Link, Email: m.To, SiteURL: site}); err != nil {
			return err
		}
	}
	via := "platform"
	if prov.SMTP != nil {
		via = "project"
	}
	id := uuid.New()
	raw, err := json.Marshal(queuedEmail{To: m.To, Subject: subject, Body: body})
	if err != nil {
		return err
	}
	sealed, err := s.keyring.Encrypt(raw, emailAAD(id))
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		if via == "platform" {
			// One project at a time, so the count holds.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "auth-email:"+svc.ProjectID.String()); err != nil {
				return err
			}
			n, err := q.CountPlatformAuthEmails(ctx, store.CountPlatformAuthEmailsParams{ProjectID: svc.ProjectID, Since: time.Now().Add(-time.Hour)})
			if err != nil {
				return err
			}
			if n >= PlatformEmailsPerHour {
				return ErrRateLimited
			}
		}
		return q.InsertAuthEmail(ctx, store.InsertAuthEmailParams{ID: id, ProjectID: svc.ProjectID, Kind: m.Kind, Via: via, MessageEnc: sealed})
	})
	if err != nil {
		return err
	}
	select {
	case s.emailKick <- struct{}{}:
	default:
	}
	return nil
}

// RunAuthEmail sends queued auth emails until ctx ends.
func (s *Service) RunAuthEmail(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		for {
			n, err := s.sendAuthEmails(ctx)
			if err != nil && ctx.Err() == nil {
				s.log.Warn("auth email", "err", err)
			}
			if n < emailBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.emailKick:
		}
	}
}

// SendAuthEmails sends what is due now (tests).
func (s *Service) SendAuthEmails(ctx context.Context) error {
	_, err := s.sendAuthEmails(ctx)
	return err
}

func (s *Service) sendAuthEmails(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		due, err := q.DueAuthEmails(ctx, emailBatch)
		if err != nil {
			return err
		}
		n = len(due)
		for _, d := range due {
			provider, err := s.sendOne(ctx, d)
			if err != nil {
				giveUp := d.Attempts+1 >= emailMaxTries
				msg := err.Error()
				if err := q.MarkAuthEmailFailed(ctx, store.MarkAuthEmailFailedParams{ID: d.ID, LastError: &msg,
					NextAttemptAt: time.Now().Add(time.Duration(1<<min(d.Attempts, 6)) * 15 * time.Second), GiveUp: giveUp}); err != nil {
					return err
				}
				if giveUp {
					if err := q.InsertMessageSend(ctx, store.InsertMessageSendParams{ProjectID: d.ProjectID, Channel: "email",
						Provider: provider, Kind: d.Kind, Status: "failed"}); err != nil {
						return err
					}
				}
				continue
			}
			if err := q.MarkAuthEmailSent(ctx, d.ID); err != nil {
				return err
			}
			if err := q.InsertMessageSend(ctx, store.InsertMessageSendParams{ProjectID: d.ProjectID, Channel: "email",
				Provider: provider, Kind: d.Kind, Status: "sent"}); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

func (s *Service) sendOne(ctx context.Context, d store.AuthEmailOutbox) (string, error) {
	provider := "platform"
	if d.Via == "project" {
		provider = "smtp"
	}
	raw, err := s.keyring.Decrypt(d.MessageEnc, emailAAD(d.ID))
	if err != nil {
		return provider, err
	}
	var m queuedEmail
	if err := json.Unmarshal(raw, &m); err != nil {
		return provider, err
	}
	msg := mail.Message{To: []string{m.To}, Subject: m.Subject, Body: m.Body,
		Headers: map[string]string{"X-PGDock-Event": "auth." + d.Kind, "Auto-Submitted": "auto-generated"}}
	sctx, cancel := context.WithTimeout(ctx, emailSendTimout)
	defer cancel()
	if d.Via == "project" {
		_, _, prov, err := s.loadAuth(ctx, d.ProjectID)
		if err != nil {
			return provider, err
		}
		if prov.SMTP == nil {
			return provider, errors.New("the project's SMTP settings were removed")
		}
		return provider, mail.Send(sctx, prov.SMTP.config(), msg)
	}
	if s.Mail == nil {
		return provider, mail.ErrNotConfigured
	}
	return provider, s.Mail.Send(sctx, msg)
}

// EmailUsage is a project's auth email sends in the last day, and how many
// of the platform's hourly allowance are left.
type EmailUsage struct {
	Sends         []store.ProjectMessageSendsRow
	PlatformLeft  int
	OwnSMTP       bool
	MonthlyActive int64
}

// AuthUsage is p's message sends and monthly active users.
func (s *Service) AuthUsage(ctx context.Context, projectID uuid.UUID) (EmailUsage, error) {
	q := store.New(s.db)
	sends, err := q.ProjectMessageSends(ctx, store.ProjectMessageSendsParams{ProjectID: projectID, Since: time.Now().Add(-24 * time.Hour)})
	if err != nil {
		return EmailUsage{}, err
	}
	n, err := q.CountPlatformAuthEmails(ctx, store.CountPlatformAuthEmailsParams{ProjectID: projectID, Since: time.Now().Add(-time.Hour)})
	if err != nil {
		return EmailUsage{}, err
	}
	_, _, prov, err := s.loadAuth(ctx, projectID)
	if err != nil {
		return EmailUsage{}, err
	}
	now := time.Now().UTC()
	mau, err := q.ProjectMAU(ctx, store.ProjectMAUParams{ProjectID: projectID, Month: monthOf(now)})
	if err != nil {
		return EmailUsage{}, err
	}
	return EmailUsage{Sends: sends, PlatformLeft: max(0, PlatformEmailsPerHour-int(n)), OwnSMTP: prov.SMTP != nil, MonthlyActive: mau}, nil
}

// recordActive counts the monthly active users in a report (V4 §12): a
// user's first sign-in or refresh in a month adds one to auth_mau.
func recordActive(ctx context.Context, q *store.Queries, users []edgeapi.ActiveUser, owners map[uuid.UUID]store.ProjectUsageOwnerRow) error {
	for _, a := range users {
		o, ok := owners[a.ProjectID]
		if !ok {
			continue
		}
		at := a.At.UTC()
		if at.IsZero() || at.After(time.Now().Add(time.Hour)) {
			at = time.Now().UTC()
		}
		n, err := q.InsertActiveUser(ctx, store.InsertActiveUserParams{ProjectID: a.ProjectID,
			Month: monthOf(at), UserID: a.UserID})
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := q.AddUsage(ctx, store.AddUsageParams{OrgID: o.OrgID, ProjectID: o.ID, Metric: tenancy.MetricAuthMAU,
			PeriodStart: at.Truncate(time.Hour), Quantity: intNumeric(1), PlanID: o.PlanID}); err != nil {
			return err
		}
	}
	return nil
}

func monthOf(t time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), Valid: true}
}
