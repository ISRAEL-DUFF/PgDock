package billing

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/israel-duff/pgdock/internal/tenancy"
)

// Plans (V3 §3.1).
const (
	PlanFree = "free"
	PlanPro  = "pro"
	PlanTeam = "team"
)

// Terms and modes (V3 §3.5).
const (
	TermMonthly  = "monthly"
	TermAnnual   = "annual"
	ModePostpaid = "postpaid"
	ModePrepaid  = "prepaid"
)

// Metrics rated per plan, above its inclusions. branch_gb_hours is part
// of shared storage and webhook attempts include retries, so neither is
// charged on its own. The dedicated_* and ha_* metrics are rated at the
// dedicated rates.
var PlanMetrics = []string{
	tenancy.MetricSharedStorage,
	tenancy.MetricBackupStorage,
	tenancy.MetricBranchHours,
	tenancy.MetricPoolerTraffic,
	tenancy.MetricWebhookSent,
	tenancy.MetricJobRunsSQL,
	tenancy.MetricJobRunsHTTP,
	// Backend services (V4 §10.1, §12).
	tenancy.MetricAPIRequests,
	tenancy.MetricAPIEgress,
	tenancy.MetricAuthMAU,
	tenancy.MetricStorageGBHours,
	tenancy.MetricStorageEgress,
	tenancy.MetricImageTransforms,
	tenancy.MetricRealtimeConnMinutes,
	tenancy.MetricRealtimeMessages,
}

// Prices is a price book's contents (V3 §3.9). Money is in kobo; unit
// prices may have fractions of a kobo (amounts are rounded per line).
type Prices struct {
	Currency  string          `json:"currency"`
	Plans     map[string]Plan `json:"plans"`
	Dedicated Dedicated       `json:"dedicated"`
	AddOns    AddOns          `json:"addons"`
}

// Plan is one plan's fee, inclusions and overage prices.
type Plan struct {
	Name string `json:"name"`
	// MonthlyMinor is the monthly fee, billed in advance.
	MonthlyMinor int64 `json:"monthly_minor"`
	// AnnualMinor is the fee for a year paid in advance; 0 when the plan
	// has no annual term.
	AnnualMinor int64 `json:"annual_minor"`
	// QuotaPlan is the V2 quota plan (limits) an org on this plan gets.
	QuotaPlan string `json:"quota_plan"`
	// Included is each metric's allowance per month, in the metric's unit.
	Included map[string]Dec `json:"included"`
	// Unit is the price per unit above the allowance. A metric with no
	// price isn't charged: on Free, its quota limits are hard.
	Unit map[string]Dec `json:"unit"`
	// PaymentTermsDays is when a transfer invoice is due (V3 §3.5).
	PaymentTermsDays int `json:"payment_terms_days"`
}

// Dedicated is the hourly price of dedicated resources, whatever the
// plan: an instance's hours are its vCPUs, RAM and disk (V3 §3.1).
type Dedicated struct {
	VCPUHour   Dec `json:"vcpu_hour"`
	RAMGBHour  Dec `json:"ram_gb_hour"`
	DiskGBHour Dec `json:"disk_gb_hour"`
}

// AddOns are the add-on prices.
type AddOns struct {
	// HAPremiumPercent is added to the HA standby's resource cost.
	HAPremiumPercent Dec `json:"ha_premium_percent"`
	// SyncReplicationHour is per hour of synchronous replication.
	SyncReplicationHour Dec `json:"sync_replication_hour"`
	// MessageMarginPercent is added to the provider cost of SMS and
	// WhatsApp codes sent through the platform's accounts (V4 §12).
	MessageMarginPercent Dec `json:"message_margin_percent"`
}

// Monthly is the plan's monthly fee on term (an annual fee spread over 12).
func (p Plan) Monthly(term string) Dec {
	if term == TermAnnual && p.AnnualMinor > 0 {
		return DecInt(p.AnnualMinor).Frac(1, 12)
	}
	return DecInt(p.MonthlyMinor)
}

// Fee is the plan's fee for one term.
func (p Plan) Fee(term string) int64 {
	if term == TermAnnual {
		return p.AnnualMinor
	}
	return p.MonthlyMinor
}

// ErrInvalidPrices is a price book that fails validation.
var ErrInvalidPrices = errors.New("invalid price book")

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPrices, fmt.Sprintf(format, a...))
}

// Validate checks a price book's contents.
func (p Prices) Validate() error {
	if p.Currency != "NGN" {
		return invalidf("currency must be NGN")
	}
	for _, name := range []string{PlanFree, PlanPro, PlanTeam} {
		if _, ok := p.Plans[name]; !ok {
			return invalidf("plan %q is missing", name)
		}
	}
	known := map[string]bool{}
	for _, m := range PlanMetrics {
		known[m] = true
	}
	for _, id := range sortedKeys(p.Plans) {
		pl := p.Plans[id]
		switch {
		case id != strings.ToLower(id) || strings.ContainsAny(id, " \t:") || id == "":
			return invalidf("plan id %q must be lower case without spaces or colons", id)
		case strings.TrimSpace(pl.Name) == "":
			return invalidf("plan %s needs a name", id)
		case pl.MonthlyMinor < 0 || pl.AnnualMinor < 0:
			return invalidf("plan %s: fees can't be negative", id)
		case pl.QuotaPlan == "":
			return invalidf("plan %s needs a quota plan", id)
		case pl.PaymentTermsDays < 0 || pl.PaymentTermsDays > 90:
			return invalidf("plan %s: payment terms must be 0 to 90 days", id)
		}
		if id == PlanFree && (pl.MonthlyMinor != 0 || pl.AnnualMinor != 0 || len(pl.Unit) > 0) {
			return invalidf("the free plan has no fee and no overages")
		}
		for _, set := range []map[string]Dec{pl.Included, pl.Unit} {
			for m, v := range set {
				if !known[m] {
					return invalidf("plan %s: %q is not a rated metric", id, m)
				}
				if v.Sign() < 0 {
					return invalidf("plan %s: %s can't be negative", id, m)
				}
			}
		}
	}
	for name, v := range map[string]Dec{
		"dedicated.vcpu_hour": p.Dedicated.VCPUHour, "dedicated.ram_gb_hour": p.Dedicated.RAMGBHour,
		"dedicated.disk_gb_hour": p.Dedicated.DiskGBHour, "addons.ha_premium_percent": p.AddOns.HAPremiumPercent,
		"addons.sync_replication_hour": p.AddOns.SyncReplicationHour, "addons.message_margin_percent": p.AddOns.MessageMarginPercent,
	} {
		if v.Sign() < 0 {
			return invalidf("%s can't be negative", name)
		}
	}
	if p.AddOns.HAPremiumPercent.Cmp(DecInt(500)) > 0 {
		return invalidf("the HA premium is a percentage (0 to 500)")
	}
	if p.AddOns.MessageMarginPercent.Cmp(DecInt(500)) > 0 {
		return invalidf("the message margin is a percentage (0 to 500)")
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Rank orders plans by monthly fee: changing to a higher one is an
// upgrade (immediate), to a lower one a downgrade (next cycle).
func (p Prices) Rank(plan, term string) Dec { return p.Plans[plan].Monthly(term) }

// DefaultPrices is the first price book, used until an admin publishes
// one. Its numbers are placeholders to replace with ones derived from
// measured costs (V3 §3.1).
func DefaultPrices() Prices {
	m := func(kv ...string) map[string]Dec {
		out := map[string]Dec{}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = D(kv[i+1])
		}
		return out
	}
	const (
		storage = tenancy.MetricSharedStorage
		backup  = tenancy.MetricBackupStorage
		branch  = tenancy.MetricBranchHours
		traffic = tenancy.MetricPoolerTraffic
		hooks   = tenancy.MetricWebhookSent
		sqlRuns = tenancy.MetricJobRunsSQL
		http    = tenancy.MetricJobRunsHTTP
		reqs    = tenancy.MetricAPIRequests
		apiOut  = tenancy.MetricAPIEgress
		mau     = tenancy.MetricAuthMAU
		files   = tenancy.MetricStorageGBHours
		fileOut = tenancy.MetricStorageEgress
		renders = tenancy.MetricImageTransforms
		rtConn  = tenancy.MetricRealtimeConnMinutes
		rtMsgs  = tenancy.MetricRealtimeMessages
	)
	return Prices{
		Currency: "NGN",
		Plans: map[string]Plan{
			PlanFree: {Name: "Free", QuotaPlan: "Personal", Included: map[string]Dec{}, Unit: map[string]Dec{}},
			PlanPro: {
				Name: "Pro", MonthlyMinor: 1_500_000, AnnualMinor: 15_000_000, QuotaPlan: "Pro", PaymentTermsDays: 7,
				// 10 GB of storage, 30 GB of backups, 10 branches all month.
				// Backend services (V4 §10.1): 5M requests, 250 GB out, 50k MAU, 50 GB of files and 250 GB
				// downloaded, 10k transforms, 1,000 connections all month, 10M realtime messages.
				Included: m(storage, "7300", backup, "21900", branch, "7300", traffic, "100", hooks, "100000", sqlRuns, "100000", http, "50000",
					reqs, "5000000", apiOut, "250", mau, "50000", files, "36500", fileOut, "250", renders, "10000", rtConn, "43800000", rtMsgs, "10000000"),
				// ₦250 per GB-month, ₦50 per backup GB-month, ₦100 per branch-month, ₦100 per GB moved, ₦20 per 1,000.
				// Services: ₦3 per 1,000 requests, ₦150 per GB out, ₦5 per MAU, ₦30 per file GB-month, ₦150 per
				// GB downloaded, ₦10 per 1,000 transforms, ₦16 per connection-month, ₦4 per 1,000 messages.
				Unit: m(storage, "34.25", backup, "6.85", branch, "13.7", traffic, "10000", hooks, "2", sqlRuns, "2", http, "2",
					reqs, "0.3", apiOut, "15000", mau, "500", files, "4.11", fileOut, "15000", renders, "1", rtConn, "0.0365", rtMsgs, "0.4"),
			},
			PlanTeam: {
				Name: "Team", MonthlyMinor: 6_000_000, AnnualMinor: 60_000_000, QuotaPlan: "Team", PaymentTermsDays: 14,
				Included: m(storage, "36500", backup, "109500", branch, "36500", traffic, "500", hooks, "500000", sqlRuns, "500000", http, "250000",
					reqs, "25000000", apiOut, "1000", mau, "200000", files, "146000", fileOut, "1000", renders, "50000", rtConn, "219000000", rtMsgs, "50000000"),
				Unit: m(storage, "27.4", backup, "5.48", branch, "10.96", traffic, "8000", hooks, "1.6", sqlRuns, "1.6", http, "1.6",
					reqs, "0.24", apiOut, "12000", mau, "400", files, "3.29", fileOut, "12000", renders, "0.8", rtConn, "0.0292", rtMsgs, "0.32"),
			},
		},
		// ₦20,000 per vCPU-month, ₦5,000 per GB of RAM, ₦250 per GB of disk.
		Dedicated: Dedicated{VCPUHour: D("2740"), RAMGBHour: D("685"), DiskGBHour: D("34.25")},
		AddOns:    AddOns{HAPremiumPercent: D("20"), SyncReplicationHour: D("1370"), MessageMarginPercent: D("20")},
	}
}
