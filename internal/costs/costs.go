// Package costs attributes what the infrastructure costs to the
// organisations that use it (V3 §5.4), keeps exchange rates, and computes
// margins per organisation and plan for the platform admin's dashboard
// (§7.2). Costs stay in the currency they are billed in (Hetzner bills in
// euros) and are shown in naira at the current rate, and at each day's
// rate, so margin eroded by the exchange rate is visible before the next
// repricing.
package costs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

// Categories of cost_allocations.
const (
	CatShared     = "shared"
	CatDedicated  = "dedicated"
	CatHA         = "ha"
	CatBackup     = "backup"
	CatEgress     = "egress"
	CatFloatingIP = "floating_ip"
	CatOverhead   = "overhead"
	CatIdle       = "idle"
)

// NGN is the naira, the currency revenue is in.
const NGN = "NGN"

// Errors.
var (
	ErrInvalid = errors.New("invalid request")
	ErrNoRate  = errors.New("no exchange rate")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Rater rates an organisation's month (billing.Service).
type Rater interface {
	Rate(ctx context.Context, orgID uuid.UUID, month time.Time) (billing.Rated, error)
}

// Service attributes costs and computes margins.
type Service struct {
	db     *pgxpool.Pool
	rater  Rater
	region string
	log    *slog.Logger
	now    func() time.Time
}

// New returns the service. region is where costs not tied to a node are
// booked.
func New(db *pgxpool.Pool, rater Rater, region string, log *slog.Logger) *Service {
	if region == "" {
		region = "eu-central"
	}
	return &Service{db: db, rater: rater, region: region, log: log, now: time.Now}
}

// SetClock sets the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// ---- Settings ----------------------------------------------------------------

const settingsKey = "costs"

// Overhead is a fixed monthly cost: the control plane, the status page,
// email.
type Overhead struct {
	Name         string `json:"name"`
	MonthlyMinor int64  `json:"monthly_minor"`
	Currency     string `json:"currency"`
}

// Settings are the costs not in the provider's server catalog.
type Settings struct {
	// Currency of the prices below (EUR).
	Currency string `json:"currency"`
	// ObjectStorageGBMonth is backup storage per GB-month, in minor units
	// (fractions allowed: Hetzner's €5.99/TB is 0.585 cents).
	ObjectStorageGBMonth float64 `json:"object_storage_gb_month_minor"`
	// EgressGB is data transfer out per GB, in minor units.
	EgressGB float64 `json:"egress_gb_minor"`
	// FloatingIPs and FloatingIPMonthly: the edge pooler's addresses.
	FloatingIPs       int   `json:"floating_ips"`
	FloatingIPMonthly int64 `json:"floating_ip_monthly_minor"`
	// Overheads are fixed monthly costs no organisation is charged with.
	Overheads []Overhead `json:"overheads"`
}

// DefaultSettings are Hetzner's list prices in euros (2026).
func DefaultSettings() Settings {
	return Settings{Currency: "EUR", ObjectStorageGBMonth: 0.585, EgressGB: 0.1, FloatingIPs: 1, FloatingIPMonthly: 357}
}

func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// Validate checks the settings.
func (st Settings) Validate() error {
	if !validCurrency(st.Currency) {
		return invalid("the currency is a three-letter code such as EUR")
	}
	if st.ObjectStorageGBMonth < 0 || st.EgressGB < 0 || st.FloatingIPs < 0 || st.FloatingIPMonthly < 0 {
		return invalid("prices and counts can't be negative")
	}
	for _, o := range st.Overheads {
		if strings.TrimSpace(o.Name) == "" || o.MonthlyMinor < 0 || !validCurrency(o.Currency) {
			return invalid("each overhead needs a name, a monthly cost and a currency")
		}
	}
	return nil
}

// Settings returns the cost settings (the defaults if none are saved).
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
		return out, fmt.Errorf("cost settings: %w", err)
	}
	return out, nil
}

// SetSettings saves the cost settings.
func (s *Service) SetSettings(ctx context.Context, st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return store.New(s.db).PutSetting(ctx, store.PutSettingParams{Key: settingsKey, Value: raw})
}

// ---- Exchange rates -----------------------------------------------------------

// SetRate records naira per unit of currency from now (or at).
func (s *Service) SetRate(ctx context.Context, currency string, ngnPerUnit float64, at *time.Time, by *uuid.UUID) (store.FxRate, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !validCurrency(currency) || currency == NGN {
		return store.FxRate{}, invalid("the currency is a three-letter code other than NGN")
	}
	if ngnPerUnit <= 0 || math.IsInf(ngnPerUnit, 0) || math.IsNaN(ngnPerUnit) {
		return store.FxRate{}, invalid("the rate is naira per unit, more than zero")
	}
	when := s.now()
	if at != nil {
		when = *at
	}
	return store.New(s.db).InsertFXRate(ctx, store.InsertFXRateParams{Currency: currency, NgnPerUnit: num(ngnPerUnit), EffectiveAt: when, Source: "manual", SetBy: by})
}

// Rates are naira per unit of each currency, as of a time.
type Rates map[string]float64

// RatesAt returns the rates in effect at t.
func (s *Service) RatesAt(ctx context.Context, t time.Time) (Rates, error) {
	rows, err := store.New(s.db).LatestFXRates(ctx, t)
	if err != nil {
		return nil, err
	}
	out := Rates{NGN: 1}
	for _, r := range rows {
		out[r.Currency] = flt(r.NgnPerUnit)
	}
	return out, nil
}

// ToNGN converts minor units of currency to naira minor units (kobo).
func (r Rates) ToNGN(minor float64, currency string) (float64, bool) {
	v, ok := r[currency]
	if !ok {
		return 0, false
	}
	return minor * v, true
}

// Convert implements capacity.Converter at the current rates.
func (s *Service) Convert(ctx context.Context, minor int64, from, to string) (int64, error) {
	if from == to {
		return minor, nil
	}
	r, err := s.RatesAt(ctx, s.now())
	if err != nil {
		return 0, err
	}
	f, ok1 := r[from]
	t, ok2 := r[to]
	if !ok1 || !ok2 {
		return 0, fmt.Errorf("%w between %s and %s: set one under Admin → Revenue → Exchange rates", ErrNoRate, from, to)
	}
	return int64(math.Round(float64(minor) * f / t)), nil
}

// ---- Attribution --------------------------------------------------------------

func num(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(f, 'f', 6, 64))
	return n
}

func flt(n pgtype.Numeric) float64 {
	f, _ := n.Float64Value()
	return f.Float64
}

func pgDate(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func daysIn(t time.Time) float64 {
	m := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return m.AddDate(0, 1, 0).Sub(m).Hours() / 24
}

type allocKey struct {
	org      uuid.UUID
	category string
	region   string
	currency string
}

type alloc struct{ amount, quantity float64 }

type ledger map[allocKey]*alloc

func (l ledger) add(org uuid.UUID, cat, region, cur string, amount, qty float64) {
	if amount == 0 && qty == 0 {
		return
	}
	k := allocKey{org, cat, region, cur}
	if l[k] == nil {
		l[k] = &alloc{}
	}
	l[k].amount += amount
	l[k].quantity += qty
}

// Attribute computes day's costs (a UTC date) and replaces its allocations.
//
// Each node costs its monthly price divided by the days in the month. A
// node's dedicated instances take the share of it their vCPUs are of the
// node's (an HA standby's share is booked as "ha"); the rest belongs to
// its shared cluster's projects, half by their share of storage and half
// by their share of connection-hours. What nothing uses is "idle". Backup
// storage is priced per GB-month, egress per GB; floating IPs and fixed
// overheads are booked to no organisation.
func (s *Service) Attribute(ctx context.Context, day time.Time) error {
	day = dayStart(day)
	end := day.AddDate(0, 0, 1)
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	q := store.New(s.db)
	nodes, err := q.CostNodesForDay(ctx, end)
	if err != nil {
		return err
	}
	insts, err := q.CostInstances(ctx)
	if err != nil {
		return err
	}
	members, err := q.CostMembers(ctx)
	if err != nil {
		return err
	}
	usage, err := q.CostUsageForDay(ctx, store.CostUsageForDayParams{DayStart: day, DayEnd: end})
	if err != nil {
		return err
	}
	conns, err := q.CostConnectionHours(ctx, store.CostConnectionHoursParams{DayStart: day, DayEnd: end})
	if err != nil {
		return err
	}
	dm := daysIn(day)
	led := ledger{}

	storage := map[uuid.UUID]float64{} // project → GB-hours on its cluster
	orgOf := map[uuid.UUID]uuid.UUID{}
	for _, u := range usage {
		orgOf[u.ProjectID] = u.OrgID
		switch u.Metric {
		case "shared_storage_gb_hours", "branch_gb_hours":
			storage[u.ProjectID] += u.Quantity
		case "backup_storage_gb_hours":
			// A GB-month is 24 × days-in-month GB-hours.
			led.add(u.OrgID, CatBackup, s.region, st.Currency, st.ObjectStorageGBMonth*u.Quantity/(24*dm), u.Quantity)
		case "pooler_transfer_gb":
			led.add(u.OrgID, CatEgress, s.region, st.Currency, st.EgressGB*u.Quantity, u.Quantity)
		}
	}
	connHours := map[uuid.UUID]float64{}
	for _, c := range conns {
		connHours[c.ProjectID] = c.ConnectionHours
	}

	// What runs where: dedicated placements (an HA instance on each of its
	// members' nodes) and the projects on each node's shared clusters.
	type placement struct {
		org   uuid.UUID
		cpus  float64
		cat   string
		valid bool
	}
	ded := map[uuid.UUID][]placement{}
	sharedOn := map[uuid.UUID][]uuid.UUID{}
	memberNodes := map[uuid.UUID][]uuid.UUID{}
	for _, m := range members {
		memberNodes[m.InstanceID] = append(memberNodes[m.InstanceID], m.NodeID)
	}
	for _, i := range insts {
		if i.Kind == "shared" {
			if i.ProjectID != nil {
				sharedOn[i.NodeID] = append(sharedOn[i.NodeID], *i.ProjectID)
				orgOf[*i.ProjectID] = *i.OrgID
			}
			continue
		}
		cpu := flt(i.CpuLimit)
		var org uuid.UUID
		if i.OrgID != nil {
			org = *i.OrgID
		}
		if mn := memberNodes[i.ID]; i.HaEnabled && len(mn) > 0 {
			for _, n := range mn {
				cat := CatHA
				if n == i.NodeID {
					cat = CatDedicated
				}
				ded[n] = append(ded[n], placement{org, cpu, cat, i.OrgID != nil})
			}
			continue
		}
		ded[i.NodeID] = append(ded[i.NodeID], placement{org, cpu, CatDedicated, i.OrgID != nil})
	}

	for _, n := range nodes {
		if n.Status == "removed" || n.MonthlyCostMinor == nil {
			continue
		}
		daily := float64(*n.MonthlyCostMinor) / dm
		var m agentapi.HostMetrics
		_ = json.Unmarshal(n.Capacity, &m)
		cpus := float64(m.CPUs)
		if cpus <= 0 {
			cpus = 1
		}
		used := 0.0
		for _, p := range ded[n.ID] {
			share := math.Min(p.cpus/cpus, 1-used)
			if share <= 0 {
				continue
			}
			used += share
			org := p.org
			if !p.valid {
				org = uuid.Nil
			}
			led.add(org, p.cat, n.Region, n.CostCurrency, daily*share, p.cpus*24)
		}
		rest := daily * (1 - used)
		if rest <= 0 {
			continue
		}
		projects := sharedOn[n.ID]
		var totalStorage, totalConn float64
		for _, p := range projects {
			totalStorage += storage[p]
			totalConn += connHours[p]
		}
		if len(projects) == 0 || (totalStorage == 0 && totalConn == 0) {
			led.add(uuid.Nil, CatIdle, n.Region, n.CostCurrency, rest, 0)
			continue
		}
		for _, p := range projects {
			var w float64
			switch {
			case totalStorage > 0 && totalConn > 0:
				w = 0.5*storage[p]/totalStorage + 0.5*connHours[p]/totalConn
			case totalStorage > 0:
				w = storage[p] / totalStorage
			default:
				w = connHours[p] / totalConn
			}
			led.add(orgOf[p], CatShared, n.Region, n.CostCurrency, rest*w, storage[p])
		}
	}
	if st.FloatingIPs > 0 {
		led.add(uuid.Nil, CatFloatingIP, s.region, st.Currency, float64(st.FloatingIPs)*float64(st.FloatingIPMonthly)/dm, float64(st.FloatingIPs))
	}
	for _, o := range st.Overheads {
		led.add(uuid.Nil, CatOverhead, s.region, o.Currency, float64(o.MonthlyMinor)/dm, 0)
	}

	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tq := store.New(tx)
		if err := tq.DeleteCostAllocations(ctx, pgDate(day)); err != nil {
			return err
		}
		for k, a := range led {
			if err := tq.InsertCostAllocation(ctx, store.InsertCostAllocationParams{
				Day: pgDate(day), OrgID: k.org, Category: k.category, Region: k.region, Currency: k.currency,
				AmountMinor: num(a.amount), Quantity: num(a.quantity),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Backfill attributes every day of this month before today that has no
// allocations yet, and yesterday again (its usage may have arrived late).
func (s *Service) Backfill(ctx context.Context) error {
	today := dayStart(s.now())
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	if today.Equal(first) {
		first = first.AddDate(0, -1, 0)
	}
	done, err := store.New(s.db).AttributedDays(ctx, store.AttributedDaysParams{FromDay: pgDate(first), ToDay: pgDate(today)})
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, d := range done {
		have[d.Time.Format(time.DateOnly)] = true
	}
	var errs []error
	for d := first; d.Before(today); d = d.AddDate(0, 0, 1) {
		if !have[d.Format(time.DateOnly)] || d.Equal(today.AddDate(0, 0, -1)) {
			errs = append(errs, s.Attribute(ctx, d))
		}
	}
	return errors.Join(errs...)
}

// Run attributes the previous days every hour until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.Backfill(ctx); err != nil {
			s.log.Warn("cost attribution", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
