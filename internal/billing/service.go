package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
)

// Mailer sends email (the mail service).
type Mailer interface {
	Send(ctx context.Context, m mail.Message) error
}

// Service is the billing core.
type Service struct {
	db        *pgxpool.Pool
	mail      Mailer
	publicURL string
	log       *slog.Logger
	// Now is the clock (tests move it).
	Now func() time.Time

	// Payments (V3 §3.4): the configured providers by name, which channel
	// goes where, and what to do once money arrives.
	providers map[string]PaymentProvider
	routing   Routing
	onPaid    func(ctx context.Context, orgID uuid.UUID)
	// onChargeFailed runs when an automatic charge is declined (dunning).
	onChargeFailed func(ctx context.Context, orgID uuid.UUID)
	seal           Sealer
	docs           DocStore
}

// Routing is which provider serves each channel (V3 §3.4.1: configuration,
// not code). Empty means the channel isn't offered.
type Routing struct {
	Cards      string // hosted checkout and saved-card charges
	VAPrimary  string // virtual accounts
	VAFallback string
	Wallet     string // Pay with iSpend and mandates
}

// Sealer encrypts card tokens at rest (the master key).
type Sealer interface {
	Encrypt(plaintext, aad []byte) ([]byte, error)
	Decrypt(ciphertext, aad []byte) ([]byte, error)
}

// SetPayments configures payment providers and routing.
func (s *Service) SetPayments(providers []PaymentProvider, r Routing, seal Sealer) {
	s.providers = map[string]PaymentProvider{}
	for _, p := range providers {
		s.providers[p.Name()] = p
	}
	s.routing = r
	s.seal = seal
}

// Provider returns a configured provider.
func (s *Service) Provider(name string) (PaymentProvider, bool) {
	p, ok := s.providers[name]
	return p, ok
}

// OnPaid runs f after each new payment (dunning re-evaluates).
func (s *Service) OnPaid(f func(ctx context.Context, orgID uuid.UUID)) { s.onPaid = f }

// New returns the billing service. mailer may be nil (no email).
func New(db *pgxpool.Pool, mailer Mailer, publicURL string, log *slog.Logger) *Service {
	return &Service{db: db, mail: mailer, publicURL: strings.TrimRight(publicURL, "/"), log: log, Now: time.Now}
}

// Errors the API maps to responses.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// ---- Settings ---------------------------------------------------------------

const settingsKey = "billing"

// Settings are the platform's billing settings: tax rates and the seller's
// details on invoices (V3 §3.6). Rates are configuration: confirm them with
// an accountant.
type Settings struct {
	VATRate Dec    `json:"vat_rate"`
	WHTRate Dec    `json:"wht_rate"`
	Seller  Seller `json:"seller"`
	// AutoIssue issues each month's drafts on the 1st; otherwise the admin
	// issues them.
	AutoIssue bool `json:"auto_issue"`
	// Stablecoin allows USDT top-ups through iSpend (V3 §3.4.5); off until
	// the regulatory position is confirmed.
	Stablecoin bool `json:"stablecoin"`
}

// Seller is PGDock's company on invoices.
type Seller struct {
	LegalName string `json:"legal_name"`
	Address   string `json:"address"`
	TIN       string `json:"tin"`
	VATNumber string `json:"vat_number"`
	Email     string `json:"email"`
}

// DefaultSettings: VAT at 7.5% and WHT at 5%. Drafts aren't issued
// automatically until an admin turns it on (when payments are live).
func DefaultSettings() Settings {
	return Settings{VATRate: D("0.075"), WHTRate: D("0.05"), Seller: Seller{LegalName: "PGDock"}}
}

// Validate checks the rates.
func (s Settings) Validate() error {
	for name, r := range map[string]Dec{"VAT": s.VATRate, "WHT": s.WHTRate} {
		if r.Sign() < 0 || r.Cmp(D("0.5")) > 0 {
			return invalid("the %s rate is a fraction between 0 and 0.5 (0.075 for 7.5%%)", name)
		}
	}
	if strings.TrimSpace(s.Seller.LegalName) == "" {
		return invalid("the seller needs a legal name")
	}
	return nil
}

// Settings returns the billing settings (the defaults if none are saved).
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	out := DefaultSettings()
	raw, err := store.New(s.db).GetSetting(ctx, settingsKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("billing settings: %w", err)
	}
	return out, nil
}

// SetSettings saves the billing settings.
func (s *Service) SetSettings(ctx context.Context, set Settings) error {
	if err := set.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return store.New(s.db).PutSetting(ctx, store.PutSettingParams{Key: settingsKey, Value: raw})
}

// ---- Price books --------------------------------------------------------------

// Book is a price book version.
type Book struct {
	Version     int32
	EffectiveAt time.Time
	Prices      Prices
	Notes       *string
	PublishedAt *time.Time
	CreatedAt   time.Time
}

func toBook(b store.PriceBook) (Book, error) {
	out := Book{Version: b.Version, EffectiveAt: b.EffectiveAt, Notes: b.Notes, PublishedAt: b.PublishedAt, CreatedAt: b.CreatedAt}
	if err := json.Unmarshal(b.Prices, &out.Prices); err != nil {
		return out, fmt.Errorf("price book %d: %w", b.Version, err)
	}
	return out, nil
}

// seedEffective is the first book's effective date: before any usage.
var seedEffective = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// Init publishes the default price book when none is published and gives
// every organisation a billing account.
func (s *Service) Init(ctx context.Context) error {
	q := store.New(s.db)
	n, err := q.CountPublishedPriceBooks(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		raw, _ := json.Marshal(DefaultPrices())
		now := s.Now()
		notes := "Default prices: replace them with ones derived from measured costs."
		if _, err := q.InsertPriceBook(ctx, store.InsertPriceBookParams{EffectiveAt: seedEffective, Prices: raw, Notes: &notes, PublishedAt: &now}); err != nil {
			return err
		}
	}
	cur, err := s.CurrentBook(ctx)
	if err != nil {
		return err
	}
	if _, err := q.EnsureAllBillingAccounts(ctx, cur.Version); err != nil {
		return err
	}
	// Billing starts with the month it was first set up in: usage from
	// before is never invoiced.
	if _, err := q.GetSetting(ctx, invoicedKey); errors.Is(err, pgx.ErrNoRows) {
		return q.PutSetting(ctx, store.PutSettingParams{Key: invoicedKey, Value: mustJSON(MonthStart(s.Now()).AddDate(0, -1, 0))})
	} else if err != nil {
		return err
	}
	return nil
}

// CurrentBook is the published book in effect now.
func (s *Service) CurrentBook(ctx context.Context) (Book, error) {
	b, err := store.New(s.db).CurrentPriceBook(ctx, s.Now())
	if errors.Is(err, pgx.ErrNoRows) {
		return Book{}, fmt.Errorf("%w: no price book is published", ErrNotFound)
	}
	if err != nil {
		return Book{}, err
	}
	return toBook(b)
}

// GetBook returns a version.
func (s *Service) GetBook(ctx context.Context, version int32) (Book, error) {
	b, err := store.New(s.db).GetPriceBook(ctx, version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Book{}, ErrNotFound
	}
	if err != nil {
		return Book{}, err
	}
	return toBook(b)
}

// Books lists every version, newest first.
func (s *Service) Books(ctx context.Context) ([]Book, error) {
	rows, err := store.New(s.db).ListPriceBooks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Book, 0, len(rows))
	for _, r := range rows {
		b, err := toBook(r)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// NoticePeriod is how far ahead a new price book must take effect (V3 §3.9).
const NoticePeriod = 30 * 24 * time.Hour

// CreateDraft saves a draft price book.
func (s *Service) CreateDraft(ctx context.Context, p Prices, effective time.Time, notes *string) (Book, error) {
	if err := p.Validate(); err != nil {
		return Book{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	raw, _ := json.Marshal(p)
	b, err := store.New(s.db).InsertPriceBook(ctx, store.InsertPriceBookParams{EffectiveAt: effective, Prices: raw, Notes: notes})
	if err != nil {
		return Book{}, err
	}
	return toBook(b)
}

// UpdateDraft edits a draft.
func (s *Service) UpdateDraft(ctx context.Context, version int32, p Prices, effective time.Time, notes *string) (Book, error) {
	if err := p.Validate(); err != nil {
		return Book{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	raw, _ := json.Marshal(p)
	b, err := store.New(s.db).UpdatePriceBookDraft(ctx, store.UpdatePriceBookDraftParams{Version: version, EffectiveAt: effective, Prices: raw, Notes: notes})
	if errors.Is(err, pgx.ErrNoRows) {
		return Book{}, s.draftMissing(ctx, version)
	}
	if err != nil {
		return Book{}, err
	}
	return toBook(b)
}

func (s *Service) draftMissing(ctx context.Context, version int32) error {
	if _, err := store.New(s.db).GetPriceBook(ctx, version); err == nil {
		return fmt.Errorf("%w: price book %d is published and can't change", ErrConflict, version)
	}
	return ErrNotFound
}

// DeleteDraft drops a draft.
func (s *Service) DeleteDraft(ctx context.Context, version int32) error {
	n, err := store.New(s.db).DeletePriceBookDraft(ctx, version)
	if err != nil {
		return err
	}
	if n == 0 {
		return s.draftMissing(ctx, version)
	}
	return nil
}

// Publish publishes a draft. It must take effect at least NoticePeriod
// ahead (unless no book is published yet), and the billing contacts of
// the orgs it will apply to are emailed the change.
func (s *Service) Publish(ctx context.Context, version int32, by *uuid.UUID) (Book, int, error) {
	q := store.New(s.db)
	b, err := s.GetBook(ctx, version)
	if err != nil {
		return Book{}, 0, err
	}
	if b.PublishedAt != nil {
		return Book{}, 0, fmt.Errorf("%w: price book %d is already published", ErrConflict, version)
	}
	n, err := q.CountPublishedPriceBooks(ctx)
	if err != nil {
		return Book{}, 0, err
	}
	if n > 0 && b.EffectiveAt.Before(s.Now().Add(NoticePeriod)) {
		return Book{}, 0, invalid("a new price book takes effect at least 30 days after it is published (V3 §3.9): move its effective date to %s or later",
			s.Now().Add(NoticePeriod).UTC().Format("2 Jan 2006"))
	}
	pub, err := q.PublishPriceBook(ctx, store.PublishPriceBookParams{Version: version, PublishedBy: by})
	if err != nil {
		return Book{}, 0, err
	}
	out, err := toBook(pub)
	if err != nil {
		return Book{}, 0, err
	}
	notified := s.notifyRepricing(ctx, out)
	return out, notified, nil
}

// Reprice moves organisations to the book now in effect, except
// grandfathered ones and annual terms until renewal (V3 §3.9).
func (s *Service) Reprice(ctx context.Context) (int, error) {
	cur, err := s.CurrentBook(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := store.New(s.db).RepriceAccounts(ctx, store.RepriceAccountsParams{Version: cur.Version, At: s.Now()})
	if len(rows) > 0 {
		s.log.Info("orgs moved to a new price book", "version", cur.Version, "orgs", len(rows))
	}
	return len(rows), err
}

// notifyRepricing emails each affected org what changes for its plan.
func (s *Service) notifyRepricing(ctx context.Context, b Book) int {
	if s.mail == nil {
		return 0
	}
	q := store.New(s.db)
	accts, err := q.ListBillingAccounts(ctx)
	if err != nil {
		s.log.Warn("repricing notice: listing accounts", "err", err)
		return 0
	}
	books := map[int32]Book{}
	sent := 0
	for _, a := range accts {
		if a.Grandfathered || a.OrgStatus != "active" {
			continue
		}
		old, ok := books[a.PriceBookVersion]
		if !ok {
			if old, err = s.GetBook(ctx, a.PriceBookVersion); err != nil {
				continue
			}
			books[a.PriceBookVersion] = old
		}
		body := comparePrices(old.Prices, b.Prices, a.Plan, a.Term)
		if body == "" {
			continue // nothing changes for this plan
		}
		when := b.EffectiveAt.UTC().Format("2 January 2006")
		if a.Term == TermAnnual && a.TermEndsAt != nil && a.TermEndsAt.After(b.EffectiveAt) {
			when = "your annual term's renewal on " + a.TermEndsAt.UTC().Format("2 January 2006")
		}
		to, err := q.BillingRecipients(ctx, a.OrgID)
		if err != nil || len(to) == 0 {
			continue
		}
		msg := mail.Message{
			To:      to,
			Subject: "PGDock prices are changing on " + b.EffectiveAt.UTC().Format("2 January 2006"),
			Body: fmt.Sprintf("Prices for %s's plan change from %s.\n\n%s\nThe full price list: %s/pricing\n",
				a.OrgName, when, body, s.publicURL),
			Headers: map[string]string{"X-PGDock-Event": "billing.repricing"},
		}
		if err := s.mail.Send(ctx, msg); err != nil {
			s.log.Warn("repricing notice", "org", a.OrgID, "err", err)
			continue
		}
		sent++
	}
	return sent
}

// comparePrices lists what changes for plan between two books, or "".
func comparePrices(old, next Prices, plan, term string) string {
	op, np := old.Plans[plan], next.Plans[plan]
	var b strings.Builder
	if f, g := op.Fee(term), np.Fee(term); f != g {
		fmt.Fprintf(&b, "  %s plan (%s): %s → %s\n", np.Name, term, Naira(f), Naira(g))
	}
	for _, m := range PlanMetrics {
		if a, c := op.Unit[m], np.Unit[m]; a.Cmp(c) != 0 {
			fmt.Fprintf(&b, "  %s: %s → %s kobo per unit\n", m, a, c)
		}
		if a, c := op.Included[m], np.Included[m]; a.Cmp(c) != 0 {
			fmt.Fprintf(&b, "  %s included: %s → %s\n", m, a, c)
		}
	}
	for name, pair := range map[string][2]Dec{
		"Dedicated vCPU-hour":    {old.Dedicated.VCPUHour, next.Dedicated.VCPUHour},
		"Dedicated RAM GB-hour":  {old.Dedicated.RAMGBHour, next.Dedicated.RAMGBHour},
		"Dedicated disk GB-hour": {old.Dedicated.DiskGBHour, next.Dedicated.DiskGBHour},
	} {
		if pair[0].Cmp(pair[1]) != 0 {
			fmt.Fprintf(&b, "  %s: %s → %s kobo\n", name, pair[0], pair[1])
		}
	}
	return b.String()
}

// Naira formats kobo as "NGN 15,000.00".
func Naira(kobo int64) string {
	neg := kobo < 0
	if neg {
		kobo = -kobo
	}
	whole := fmt.Sprintf("%d", kobo/100)
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	s := fmt.Sprintf("NGN %s.%02d", b.String(), kobo%100)
	if neg {
		s = "-" + s
	}
	return s
}

// Run applies scheduled plan changes, renews annual terms, moves orgs to
// new price books and runs the monthly invoicing every interval, and
// refreshes forecasts hourly, until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var forecastAt, requeryAt time.Time
	var daily time.Time   // the last day the daily work ran
	var nightly time.Time // the last day re-queried in full
	for {
		// Prepaid deductions, balance alerts, card reminders: daily, after
		// midnight's usage is recorded.
		if now := s.Now(); now.UTC().Hour() >= 1 && daily.Before(dayStart(now)) {
			if err := s.Daily(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("billing: daily", "err", err)
			}
			daily = dayStart(now)
		}
		// Missed provider events (V3 §3.4.8): the last two hours hourly, the
		// whole previous day once a night.
		if now := s.Now(); len(s.providers) > 0 && now.Sub(requeryAt) >= time.Hour {
			if _, err := s.Requery(ctx, now.Add(-2*time.Hour), now); err != nil && ctx.Err() == nil {
				s.log.Warn("billing: re-query", "err", err)
			}
			requeryAt = now
			if y := dayStart(now).AddDate(0, 0, -1); now.UTC().Hour() >= 1 && nightly.Before(y) {
				if _, err := s.Requery(ctx, y, y.AddDate(0, 0, 1)); err != nil && ctx.Err() == nil {
					s.log.Warn("billing: nightly re-query", "err", err)
				} else {
					nightly = y
				}
			}
		}
		// Forecasts, budget alerts and the spend cap: hourly (V3 §3.10).
		if now := s.Now(); now.Sub(forecastAt) >= time.Hour {
			if err := s.RefreshForecasts(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("billing: forecasts", "err", err)
			}
			forecastAt = now
		}
		if _, err := s.ApplyDueChanges(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("billing: plan changes", "err", err)
		}
		if _, err := s.Reprice(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("billing: repricing", "err", err)
		}
		if err := s.Invoicing(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("billing: invoicing", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
