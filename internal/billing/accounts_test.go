package billing_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

type sentMail struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (m *sentMail) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	return nil
}

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func newService(t *testing.T) (*billing.Service, *pgxpool.Pool, *clock, *sentMail) {
	t.Helper()
	db := storetest.New(t)
	m := &sentMail{}
	clk := &clock{t: time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC)}
	s := billing.New(db, m, "https://pgdock.example", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Now = clk.Now
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, db, clk, m
}

func quotaPlan(t *testing.T, db *pgxpool.Pool, org uuid.UUID) string {
	t.Helper()
	n, err := store.New(db).OrgQuotaPlanName(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAccountsAndPlanChanges(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")

	a, err := s.Account(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if a.Plan != billing.PlanFree || a.PriceBookVersion != 1 || a.Mode != billing.ModePostpaid {
		t.Fatalf("new account %+v", a)
	}

	// Free → Pro on the 13th: the rest of October, at once.
	dry, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro, DryRun: true})
	if err != nil || !dry.Applied || !dry.Upgrade || dry.Total != 919355 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if a, _ := s.Account(ctx, org); a.Plan != billing.PlanFree {
		t.Fatal("a dry run changed the plan")
	}
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, org); a.Plan != billing.PlanPro || a.PaymentTermsDays != 7 {
		t.Errorf("after the upgrade: %+v", a)
	}
	if q := quotaPlan(t, db, org); q != "Pro" {
		t.Errorf("quota plan %s, want Pro", q)
	}
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); !errors.Is(err, billing.ErrConflict) {
		t.Errorf("same plan again: %v", err)
	}

	// Pro → Team the same day: credit the unused Pro, charge Team.
	up, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanTeam})
	if err != nil || up.Total != 3677419-919355 || len(up.Lines) != 2 {
		t.Fatalf("upgrade: %+v %v", up, err)
	}

	// Team → Pro is a downgrade: from 1 November.
	down, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro})
	if err != nil || down.Applied || len(down.Lines) != 0 || !down.EffectiveAt.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("downgrade: %+v %v", down, err)
	}
	if a, _ := s.Account(ctx, org); a.Plan != billing.PlanTeam {
		t.Error("the downgrade applied early")
	}
	if n, err := s.ApplyDueChanges(ctx); err != nil || n != 0 {
		t.Fatalf("early sweep: %d %v", n, err)
	}
	clk.t = time.Date(2026, 11, 1, 0, 5, 0, 0, time.UTC)
	if n, err := s.ApplyDueChanges(ctx); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if a, _ := s.Account(ctx, org); a.Plan != billing.PlanPro {
		t.Errorf("after the sweep: %s", a.Plan)
	}
	if q := quotaPlan(t, db, org); q != "Pro" {
		t.Errorf("quota plan %s", q)
	}

	// An admin's custom quota plan survives plan changes.
	if _, err := db.Exec(ctx, `UPDATE organizations SET plan_id = (SELECT id FROM quota_plans WHERE name = 'Unlimited') WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanTeam}); err != nil {
		t.Fatal(err)
	}
	if q := quotaPlan(t, db, org); q != "Unlimited" {
		t.Errorf("quota plan %s, want the admin's Unlimited", q)
	}

	// Annual: paid at once, renewed when the term ends.
	ann, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanTeam, Term: billing.TermAnnual})
	if err != nil || !ann.Applied || ann.TermEndsAt == nil {
		t.Fatalf("annual: %+v %v", ann, err)
	}
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanFree, Term: billing.TermAnnual}); err == nil {
		t.Error("free has no annual term")
	}
	clk.t = ann.TermEndsAt.Add(time.Hour)
	if n, err := s.ApplyDueChanges(ctx); err != nil || n != 1 {
		t.Fatalf("renewal: %d %v", n, err)
	}
	a, _ = s.Account(ctx, org)
	if a.TermEndsAt == nil || !a.TermEndsAt.Equal(ann.TermEndsAt.AddDate(1, 0, 0)) {
		t.Errorf("renewed term ends %v", a.TermEndsAt)
	}
}

func TestPriceBookPublishing(t *testing.T) {
	s, db, clk, sent := newService(t)
	ctx := context.Background()
	orgs := map[string]uuid.UUID{}
	for _, slug := range []string{"monthly", "annual", "grandfathered", "free"} {
		orgs[slug] = newOrg(t, db, slug)
		if _, err := s.Account(ctx, orgs[slug]); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddContact(ctx, orgs[slug], "Finance@"+slug+".example", nil); err != nil {
			t.Fatal(err)
		}
	}
	for slug, term := range map[string]string{"monthly": billing.TermMonthly, "annual": billing.TermAnnual, "grandfathered": billing.TermMonthly} {
		if _, err := s.ChangePlan(ctx, orgs[slug], billing.PlanRequest{Plan: billing.PlanPro, Term: term}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE billing_accounts SET grandfathered = true WHERE org_id = $1`, orgs["grandfathered"]); err != nil {
		t.Fatal(err)
	}

	p := billing.DefaultPrices()
	pro := p.Plans[billing.PlanPro]
	pro.MonthlyMinor, pro.AnnualMinor = 2_000_000, 20_000_000
	p.Plans[billing.PlanPro] = pro
	soon := clk.t.Add(10 * 24 * time.Hour)
	draft, err := s.CreateDraft(ctx, p, soon, nil)
	if err != nil || draft.Version != 2 || draft.PublishedAt != nil {
		t.Fatalf("draft: %+v %v", draft, err)
	}
	if _, _, err := s.Publish(ctx, draft.Version, nil); !errors.Is(err, billing.ErrInvalid) {
		t.Fatalf("publishing 10 days ahead: %v", err)
	}
	later := clk.t.Add(31 * 24 * time.Hour)
	if _, err := s.UpdateDraft(ctx, draft.Version, p, later, nil); err != nil {
		t.Fatal(err)
	}
	pub, notified, err := s.Publish(ctx, draft.Version, nil)
	if err != nil || pub.PublishedAt == nil {
		t.Fatalf("publish: %v", err)
	}
	// The monthly and annual Pro orgs hear about it; not the grandfathered
	// org nor Free (whose prices don't change).
	if notified != 2 || len(sent.msgs) != 2 {
		t.Fatalf("notified %d: %+v", notified, sent.msgs)
	}
	for _, m := range sent.msgs {
		if !strings.Contains(m.Body, "NGN 15,000.00 → NGN 20,000.00") && !strings.Contains(m.Body, "NGN 150,000.00 → NGN 200,000.00") {
			t.Errorf("notice body: %s", m.Body)
		}
	}
	if _, err := s.UpdateDraft(ctx, draft.Version, p, later, nil); !errors.Is(err, billing.ErrConflict) {
		t.Errorf("editing a published book: %v", err)
	}

	// Before the date nothing moves; after it, all but grandfathered and
	// running annual terms do.
	if n, err := s.Reprice(ctx); err != nil || n != 0 {
		t.Fatalf("early reprice: %d %v", n, err)
	}
	clk.t = later.Add(time.Hour)
	if n, err := s.Reprice(ctx); err != nil || n != 2 {
		t.Fatalf("reprice: %d %v", n, err)
	}
	for slug, want := range map[string]int32{"monthly": 2, "free": 2, "annual": 1, "grandfathered": 1} {
		if a, _ := s.Account(ctx, orgs[slug]); a.PriceBookVersion != want {
			t.Errorf("%s on version %d, want %d", slug, a.PriceBookVersion, want)
		}
	}
	// A new org gets the current book.
	if a, _ := s.Account(ctx, newOrg(t, db, "newcomer")); a.PriceBookVersion != 2 {
		t.Errorf("new org on version %d", a.PriceBookVersion)
	}
}

func TestContacts(t *testing.T) {
	s, db, _, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	if _, err := s.AddContact(ctx, org, "not an email", nil); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("bad address: %v", err)
	}
	if _, err := s.AddContact(ctx, org, "Ada <ada@example.com>", nil); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("named address: %v", err)
	}
	if c, err := s.AddContact(ctx, org, "AP@Example.com", nil); err != nil || c.Email != "ap@example.com" {
		t.Fatalf("add: %+v %v", c, err)
	}
	if err := s.RemoveContact(ctx, org, "ap@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveContact(ctx, org, "ap@example.com"); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
}
