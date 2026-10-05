package incidents

import (
	"context"

	"github.com/israel-duff/pgdock/internal/alerts"
	"github.com/israel-duff/pgdock/internal/statusapi"
	"github.com/israel-duff/pgdock/internal/store"
)

// States is what pgdock-server knows about the components pgdock-status
// can't probe. That a heartbeat arrives at all also says pgdock-server's
// workers are running.
func (s *Service) States(ctx context.Context) ([]statusapi.ComponentState, error) {
	q := store.New(s.db)
	firing, err := q.FiringAlerts(ctx)
	if err != nil {
		return nil, err
	}
	// The page is public: say what is wrong, not how much or for whom.
	backups := statusapi.ComponentState{ID: ComponentBackups, Status: statusapi.Operational}
	for _, a := range firing {
		switch a.Kind {
		case alerts.KindBackupFailed, alerts.KindBackupOverdue, alerts.KindRestoreTestFailed:
			backups.Status, backups.Detail = statusapi.Degraded, "Some backups are delayed or failing."
		}
	}

	nodes, err := q.DedicatedNodeHealth(ctx)
	if err != nil {
		return nil, err
	}
	dedicated := statusapi.ComponentState{ID: ComponentDedicated, Status: statusapi.Operational}
	switch {
	case nodes.Total > 0 && nodes.Unreachable == nodes.Total:
		dedicated.Status, dedicated.Detail = statusapi.Down, "Dedicated instance hosts are unreachable."
	case nodes.Unreachable > 0:
		dedicated.Status, dedicated.Detail = statusapi.Degraded, "Some dedicated instance hosts are unreachable."
	}

	overdue, err := q.OverdueJobs(ctx)
	if err != nil {
		return nil, err
	}
	automation := statusapi.ComponentState{ID: ComponentWebhooksJobs, Status: statusapi.Operational}
	if overdue > 0 {
		automation.Status, automation.Detail = statusapi.Degraded, "Scheduled jobs are running late."
	}

	var out []statusapi.ComponentState
	for _, c := range []statusapi.ComponentState{dedicated, backups, automation} {
		for _, id := range s.cfg.Components {
			if id == c.ID {
				out = append(out, c)
			}
		}
	}
	return out, nil
}
