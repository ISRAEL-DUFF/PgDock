package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Prices are in US dollars (cents) and money moves in naira (kobo): an
// invoice is converted at the rate in force when it is issued, and the whole
// quote is kept on it. Both currencies have two decimals, so the
// kobo-per-cent rate is the naira-per-dollar rate itself.

// Price currencies. A price book in naira needs no conversion (the rate is 1).
const (
	CurrencyUSD = "USD"
	CurrencyNGN = "NGN"
)

// DefaultFXMaxAge is how old a rate may be when an invoice is converted: a
// provider that can't price a pair answers with its last rate, however old.
const DefaultFXMaxAge = 4 * time.Hour

// FXQuote is the rate an amount was converted at (V4.1 billing: wallet
// provider's quote). It is stored on invoices and payments as it was.
type FXQuote struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Market    Dec       `json:"market_rate"`
	Effective Dec       `json:"effective_rate"` // the rate used: Market with the spread applied
	SpreadBPS int       `json:"spread_bps"`
	FetchedAt time.Time `json:"fetched_at"` // when the provider last priced the pair
	Sources   []string  `json:"sources,omitempty"`
	Derived   bool      `json:"derived,omitempty"`
	// QuotedAt is when PGDock asked.
	QuotedAt time.Time `json:"quoted_at"`
}

// Naira converts minor units of the price currency to kobo.
func (q FXQuote) Naira(minor int64) int64 {
	if q.Effective.IsZero() {
		return minor
	}
	return DecInt(minor).Mul(q.Effective).Round()
}

// Dollars converts kobo back to cents (for lines whose cost is in naira).
func (q FXQuote) Dollars(kobo int64) int64 {
	if q.Effective.IsZero() {
		return kobo
	}
	return DecInt(kobo).Quo(q.Effective).Round()
}

// identityQuote is the rate of a naira price book: 1.
func identityQuote(now time.Time) FXQuote {
	return FXQuote{From: CurrencyNGN, To: CurrencyNGN, Market: DecInt(1), Effective: DecInt(1), FetchedAt: now, QuotedAt: now}
}

// FX errors.
var (
	// ErrNoRate is no rate source: no wallet provider is configured.
	ErrNoRate = errors.New("no exchange rate is available: no wallet provider is configured")
	// ErrStaleRate is a rate older than the platform accepts for an invoice.
	ErrStaleRate = errors.New("the exchange rate is too old to convert an invoice")
)

// RateSource prices a currency pair (the wallet provider; a fixed one in tests).
type RateSource interface {
	Rate(ctx context.Context, from, to string, amountMinor int64) (FXRate, error)
}

// SetRateSource sets where the dollar's price comes from when there is no
// wallet provider to ask (tests, and platforms that only issue invoices).
func (s *Service) SetRateSource(r RateSource) { s.rates = r }

type fxCache struct {
	mu   sync.Mutex
	last FXQuote
	at   time.Time
	ok   bool
}

// fxTTL is how long an indicative rate (a draft, a forecast, a preview) is
// reused.
const fxTTL = 2 * time.Minute

// Quote returns the dollar's price in naira. Strict is for converting an
// invoice or a charge: a fresh answer, no older than the configured age;
// otherwise a recent one is reused and, when the provider can't answer, the
// last one known at any age (an estimate is better than none).
func (s *Service) Quote(ctx context.Context, strict bool) (FXQuote, error) {
	if s.rates == nil {
		return FXQuote{}, ErrNoRate
	}
	now := s.Now()
	c := &s.fx
	c.mu.Lock()
	cached, at, ok := c.last, c.at, c.ok
	c.mu.Unlock()
	if !strict && ok && now.Sub(at) < fxTTL {
		return cached, nil
	}
	r, err := s.rates.Rate(ctx, CurrencyUSD, CurrencyNGN, 100)
	if err != nil {
		if !strict && ok {
			return cached, nil
		}
		return FXQuote{}, fmt.Errorf("exchange rate: %w", err)
	}
	q := FXQuote{From: r.From, To: r.To, Market: r.Market, Effective: r.Effective, SpreadBPS: r.SpreadBPS,
		FetchedAt: r.FetchedAt, Sources: r.Sources, Derived: r.Derived, QuotedAt: now}
	if q.Effective.Sign() <= 0 {
		return FXQuote{}, errors.New("exchange rate: the provider returned no rate")
	}
	if strict {
		set, err := s.Settings(ctx)
		if err != nil {
			return FXQuote{}, err
		}
		if age := now.Sub(q.FetchedAt); age > set.fxMaxAge() {
			return FXQuote{}, fmt.Errorf("%w: it was set %s ago (%s)", ErrStaleRate, age.Round(time.Minute), strings.Join(q.Sources, ", "))
		}
	}
	c.mu.Lock()
	c.last, c.at, c.ok = q, now, true
	c.mu.Unlock()
	return q, nil
}

// quoteFor is the quote to convert an amount priced in currency: the
// identity for naira, the dollar's price otherwise.
func (s *Service) quoteFor(ctx context.Context, currency string, strict bool) (FXQuote, error) {
	if currency == CurrencyNGN {
		return identityQuote(s.Now()), nil
	}
	return s.Quote(ctx, strict)
}

// fxMaxAge is how old a rate may be for an invoice.
func (s Settings) fxMaxAge() time.Duration {
	if s.FXMaxAgeHours <= 0 {
		return DefaultFXMaxAge
	}
	return time.Duration(s.FXMaxAgeHours) * time.Hour
}
