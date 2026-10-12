package billing_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

// fakeSuspender suspends in the database, as tenancy does.
type fakeSuspender struct {
	db        *pgxpool.Pool
	mu        sync.Mutex
	suspended []uuid.UUID
	restored  []uuid.UUID
}

func (f *fakeSuspender) Suspend(ctx context.Context, org uuid.UUID, reason string) error {
	f.mu.Lock()
	f.suspended = append(f.suspended, org)
	f.mu.Unlock()
	_, err := f.db.Exec(ctx, `UPDATE organizations SET status = 'suspended', suspended_reason = $2 WHERE id = $1`, org, reason)
	return err
}

func (f *fakeSuspender) Reinstate(ctx context.Context, org uuid.UUID) error {
	f.mu.Lock()
	f.restored = append(f.restored, org)
	f.mu.Unlock()
	_, err := f.db.Exec(ctx, `UPDATE organizations SET status = 'active', suspended_reason = NULL WHERE id = $1`, org)
	return err
}

func state(t *testing.T, db *pgxpool.Pool, org uuid.UUID) string {
	t.Helper()
	s, err := store.New(db).OrgDunningState(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOverdueLadderAndReversal(t *testing.T) {
	s, db, clk, sent := newService(t)
	ctx := context.Background()
	sus := &fakeSuspender{db: db}
	removed := 0
	s.SetDunning(sus, func(context.Context, uuid.UUID) (int, error) { removed++; return 1, nil })
	org := newOrg(t, db, "acme")
	if _, err := s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, s, clk, org) // issued 1 Nov 03:00, due 8 Nov (Pro: 7 days)
	due := *inv.DueAt

	at := func(days int, want string) {
		t.Helper()
		clk.t = due.Add(time.Duration(days)*24*time.Hour + time.Hour)
		if err := s.RunDunning(ctx); err != nil {
			t.Fatal(err)
		}
		if got := state(t, db, org); got != want {
			t.Fatalf("day %d: %s, want %s", days, got, want)
		}
	}
	clk.t = due.Add(-time.Hour)
	if err := s.RunDunning(ctx); err != nil || state(t, db, org) != billing.DunningOK {
		t.Fatalf("before the due date: %s %v", state(t, db, org), err)
	}
	at(0, billing.DunningOverdue)
	at(1, billing.DunningOverdue)
	at(3, billing.DunningRestricted)
	at(7, billing.DunningRestricted)
	if len(sus.suspended) != 0 {
		t.Fatal("suspended early")
	}
	at(10, billing.DunningSuspended)
	if len(sus.suspended) != 1 {
		t.Fatalf("suspensions: %d", len(sus.suspended))
	}
	at(11, billing.DunningSuspended) // nothing twice
	if len(sus.suspended) != 1 {
		t.Fatalf("suspended twice")
	}
	at(40, billing.DunningSuspended)
	a, _ := s.Account(ctx, org)
	if a.DeletionScheduledAt == nil {
		t.Fatal("no deletion scheduled at day 40")
	}
	// The deletion: with the setting off, nothing is deleted.
	at(48, billing.DunningSuspended)
	if removed != 0 {
		t.Fatal("deleted with deletion for non-payment off")
	}
	var subjects []string
	for _, m := range sent.msgs {
		subjects = append(subjects, m.Subject)
	}
	for _, want := range []string{"overdue balance", "can't create new resources", "final notice", "suspended for non-payment", "will be deleted"} {
		if !strings.Contains(strings.Join(subjects, "|"), want) {
			t.Errorf("no %q email: %v", want, subjects)
		}
	}

	// Paying lifts it all: reinstated, back to ok, the deletion cancelled.
	if _, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderBank, Channel: billing.ChannelManual, ProviderRef: "late", AmountMinor: inv.TotalMinor}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, db, org); got != billing.DunningOK || len(sus.restored) != 1 {
		t.Fatalf("after paying: %s, reinstated %d", got, len(sus.restored))
	}
	if a, _ := s.Account(ctx, org); a.DeletionScheduledAt != nil {
		t.Error("the deletion is still scheduled")
	}
	mustCheck(t, db)
}

func TestDunningFreeGraceAndDeletion(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	sus := &fakeSuspender{db: db}
	removed := 0
	s.SetDunning(sus, func(context.Context, uuid.UUID) (int, error) { removed++; return 1, nil })

	// A Free org owing for dedicated hours is never suspended.
	free := newOrg(t, db, "free")
	if _, err := db.Exec(ctx, `INSERT INTO usage_records (org_id, project_id, metric, granularity, period_start, quantity, plan_id)
		VALUES ($1, gen_random_uuid(), 'dedicated_vcpu_hours', 'hour', $2, 100, (SELECT plan_id FROM organizations WHERE id = $1))`, free, day(5)); err != nil {
		t.Fatal(err)
	}
	clk.t = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	d, err := s.Draft(ctx, free, day(1))
	if err != nil || d == nil {
		t.Fatalf("draft: %v", err)
	}
	inv, err := s.Issue(ctx, d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	clk.t = inv.DueAt.Add(50 * 24 * time.Hour)
	if err := s.RunDunning(ctx); err != nil {
		t.Fatal(err)
	}
	if got := state(t, db, free); got != billing.DunningRestricted || len(sus.suspended) != 0 {
		t.Fatalf("free org: %s, suspended %d", got, len(sus.suspended))
	}

	// Grace from the admin holds the ladder.
	org := newOrg(t, db, "acme")
	pro := issuedProInvoice(t, s, clk, org)
	until := pro.DueAt.Add(20 * 24 * time.Hour)
	clk.t = pro.DueAt.Add(-time.Hour)
	if err := s.ExtendGrace(ctx, org, &until); err != nil {
		t.Fatal(err)
	}
	clk.t = pro.DueAt.Add(15 * 24 * time.Hour)
	if err := s.RunDunning(ctx); err != nil {
		t.Fatal(err)
	}
	if got := state(t, db, org); got != billing.DunningOK {
		t.Fatalf("during grace: %s", got)
	}

	// With deletion for non-payment on, resources go after the notice.
	set, _ := s.Settings(ctx)
	set.DeleteForNonPayment = true
	_ = s.SetSettings(ctx, set)
	for _, days := range []int{21, 25, 40, 48} {
		clk.t = pro.DueAt.Add(time.Duration(days) * 24 * time.Hour)
		if err := s.RunDunning(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if removed != 1 {
		t.Fatalf("removals %d", removed)
	}
}

func TestCardRetries(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	w.s.SetDunning(&fakeSuspender{db: w.db}, nil)
	org := newOrg(t, w.db, "acme")
	if _, err := w.s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	in, _ := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelCard, Purpose: billing.PurposeCardSetup})
	_, _ = w.flw.CompleteCheckout(in.Reference, true)
	w.event(t)
	token := "flw-t1-" + in.Reference
	w.flw.Decline(token, "Insufficient funds")

	inv := issuedProInvoice(t, w.s, w.clk, org) // the charge on issue is declined
	a, _ := w.s.Account(ctx, org)
	if a.CardFailingSince == nil || a.DunningState != billing.DunningRetrying {
		t.Fatalf("after the declined charge: %+v", a)
	}
	start := *a.CardFailingSince
	w.clk.t = start.Add(3*24*time.Hour + time.Hour)
	if err := w.s.RunDunning(ctx); err != nil { // day 3: declined again
		t.Fatal(err)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status == billing.StatusPaid { // the ₦100 card-setup credit paid a little
		t.Fatalf("day 3: %s", got.Status)
	}
	w.flw.Accept(token)
	w.clk.t = start.Add(5*24*time.Hour + time.Hour)
	if err := w.s.RunDunning(ctx); err != nil { // day 5: paid
		t.Fatal(err)
	}
	w.event(t)
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Fatalf("day 5: %s", got.Status)
	}
	if a, _ := w.s.Account(ctx, org); a.CardFailingSince != nil || a.DunningState != billing.DunningOK {
		t.Errorf("after the retry: %+v", a)
	}
	n := 0
	for _, m := range w.sent.msgs {
		if strings.Contains(m.Subject, "payment for acme failed") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d failure emails, want 2 (day 0 and day 3)", n)
	}
	mustCheck(t, w.db)
}

type memDocs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (d *memDocs) Put(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	d.mu.Lock()
	d.m[key] = b
	d.mu.Unlock()
	return err
}

func (d *memDocs) Get(_ context.Context, key string) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.m[key]
	if !ok {
		return nil, errors.New("no such document")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func TestWHTCertificate(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	docs := &memDocs{m: map[string][]byte{}}
	s.SetDocStore(docs)
	org := newOrg(t, db, "acme")
	if _, err := s.UpdateDetails(ctx, org, billing.Details{DeductsWHT: true}); err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, s, clk, org)
	pdf := []byte("%PDF-1.4\n1 0 obj << >> endobj\n%%EOF")
	if _, err := s.UploadWHTCertificate(ctx, org, inv.ID, "wht.pdf", bytes.NewReader(pdf), nil); !errors.Is(err, billing.ErrConflict) {
		t.Fatalf("before WHT was deducted: %v", err)
	}
	if _, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderBank, Channel: billing.ChannelManual, ProviderRef: "t1",
		AmountMinor: inv.TotalMinor - inv.WhtExpectedMinor}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.OutstandingWHT(ctx); len(rows) != 1 {
		t.Fatalf("outstanding WHT: %d", len(rows))
	}
	if _, err := s.UploadWHTCertificate(ctx, org, inv.ID, "x.exe", strings.NewReader("MZ\x90\x00binary"), nil); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("an executable: %v", err)
	}
	c, err := s.UploadWHTCertificate(ctx, org, inv.ID, "../../WHT credit note.pdf", bytes.NewReader(pdf), nil)
	if err != nil || c.Filename != "WHT credit note.pdf" {
		t.Fatalf("upload: %+v %v", c, err)
	}
	if got := invoiceNow(t, store.New(db), inv.ID); got.Status != billing.StatusPaid || got.WhtEvidencedAt == nil {
		t.Errorf("after the credit note: %s %v", got.Status, got.WhtEvidencedAt)
	}
	if rows, _ := s.OutstandingWHT(ctx); len(rows) != 0 {
		t.Errorf("still outstanding: %d", len(rows))
	}
	r, err := s.OpenDocument(ctx, c.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(r); !bytes.Equal(b, pdf) {
		t.Error("the document changed")
	}
	var csv bytes.Buffer
	if err := s.WHTReceivableCSV(ctx, &csv); err != nil || !strings.Contains(csv.String(), "1500.00") || !strings.Contains(csv.String(), *inv.Number) {
		t.Errorf("export: %v\n%s", err, csv.String())
	}
}

func TestReconciliation(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	org := newOrg(t, w.db, "acme")
	va, err := w.s.EnsureVirtualAccount(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	for _, amt := range []int64{100_000, 250_000} {
		if _, err := w.isp.Transfer(va.AccountNumber, amt); err != nil {
			t.Fatal(err)
		}
		w.event(t)
	}
	saveCard(t, w, org)
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	res, err := w.s.Reconcile(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if len(r.Differences) != 0 || r.Matched == 0 {
			t.Errorf("%s: matched %d, differences %+v", r.Provider, r.Matched, r.Differences)
		}
	}
	// A missed transfer and a payment the provider doesn't know both show.
	w.isp.DropWebhooks(1)
	if _, err := w.isp.Transfer(va.AccountNumber, 5_000); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderISpend, Channel: billing.ChannelTransfer, ProviderRef: "txn_ghost", AmountMinor: 1_000, ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	res, _ = w.s.Reconcile(ctx, from, to)
	kinds := map[string]bool{}
	for _, r := range res {
		for _, d := range r.Differences {
			kinds[d.Kind] = true
		}
	}
	if !kinds["missing_in_pgdock"] || !kinds["missing_at_provider"] {
		t.Errorf("differences: %+v", res)
	}
	if last, _ := w.s.LastReconciliation(ctx); len(last) != 2 {
		t.Errorf("stored result: %+v", last)
	}
}
