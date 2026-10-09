package incidents

import (
	"context"
	"slices"
	"time"

	"github.com/israel-duff/pgdock/internal/statusapi"
	"github.com/israel-duff/pgdock/internal/store"
)

// syncEvery is how often the managed subscribers are sent (V4.1 §7.1).
const syncEvery = time.Hour

// Subscribers is the status page's managed subscriber list: for each
// paying organisation (not Free, active), its owners and the billing
// contacts with status emails on, with the components its projects use
// in their regions. An address in several organisations gets the union.
func (s *Service) Subscribers(ctx context.Context) (statusapi.ManagedSubscribers, error) {
	rows, err := store.New(s.db).StatusSubscriberRows(ctx)
	if err != nil {
		return statusapi.ManagedSubscribers{}, err
	}
	type acc struct{ comps, regions []string }
	by := map[string]*acc{}
	var order []string
	add := func(list *[]string, v string) {
		if v != "" && !slices.Contains(*list, v) {
			*list = append(*list, v)
		}
	}
	for _, r := range rows {
		a := by[r.Email]
		if a == nil {
			a = &acc{}
			by[r.Email] = a
			order = append(order, r.Email)
		}
		// Every project uses the dashboard, billing and backups.
		used := []string{"dashboard", ComponentBilling, ComponentBackups}
		if r.Tier == "dedicated" {
			used = append(used, ComponentDedicated, "edge-pooler")
		} else {
			used = append(used, "shared-tier", "edge-pooler")
		}
		if r.Ha {
			used = append(used, ComponentDedicated)
		}
		if r.Services {
			used = append(used, ComponentBackendServices)
		}
		used = append(used, ComponentWebhooksJobs)
		for _, c := range used {
			if slices.Contains(s.cfg.Components, c) {
				add(&a.comps, c)
			}
		}
		add(&a.regions, r.Region)
	}
	out := statusapi.ManagedSubscribers{Subscribers: []statusapi.ManagedSubscriber{}}
	for _, e := range order {
		a := by[e]
		if len(a.comps) == 0 {
			continue
		}
		slices.Sort(a.comps)
		slices.Sort(a.regions)
		out.Subscribers = append(out.Subscribers, statusapi.ManagedSubscriber{Email: e, Components: a.comps, Regions: a.regions})
	}
	return out, nil
}

// SyncSubscribers sends the managed subscribers to pgdock-status.
func (s *Service) SyncSubscribers(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	list, err := s.Subscribers(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return s.client.PutManagedSubscribers(sctx, list)
}
