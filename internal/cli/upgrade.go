package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/client"
)

func printUpgradeChecks(w io.Writer, pf *client.UpgradePreflight) {
	mark := map[client.UpgradeCheckStatus]string{client.UpgradeCheckStatusOk: "ok", client.UpgradeCheckStatusWarning: "WARN", client.UpgradeCheckStatusBlocked: "BLOCKED"}
	for _, c := range pf.Checks {
		fmt.Fprintf(w, "  %-7s %-11s %s\n", mark[c.Status], c.Name, c.Message)
	}
	how := "by logical replication; writes pause for the switch"
	if pf.CopyMode == client.UpgradePreflightCopyModeDump {
		how = "while writes wait"
		if pf.FallbackReason != nil {
			how += " (" + *pf.FallbackReason + ")"
		}
	}
	fmt.Fprintf(w, "%s is copied %s: about %ds.\n", humanBytes(pf.SizeBytes), how, pf.EstimatedDowntimeSeconds)
}

// upgrade moves a project to a newer Postgres major (V3 §2.4).
func (a *App) upgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	to := fs.Int("to", 0, "the Postgres major to upgrade to")
	checkOnly := fs.Bool("check", false, "only run the preflight")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "upgrade <project> --to <major> [--check]"); err != nil {
		return err
	}
	if *to == 0 {
		return usageErrorf("--to takes the Postgres major to upgrade to, e.g. --to 18")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	req := client.UpgradeRequest{PgVersion: *to}
	pr, err := a.api.UpgradePreflightWithResponse(c, p.Id, req)
	if err := check(pr, err); err != nil {
		return err
	}
	pf := pr.JSON200
	title := fmt.Sprintf("Upgrading %s from Postgres %d to %d:\n", p.Name, pf.From, pf.To)
	if *checkOnly {
		return a.emit(pf, func(w io.Writer) {
			fmt.Fprint(w, title)
			printUpgradeChecks(w, pf)
		})
	}
	if !a.json {
		fmt.Fprint(a.Stderr, title)
		printUpgradeChecks(a.Stderr, pf)
	}
	if !pf.Eligible {
		return &apiError{Status: 409, Code: "conflict", Message: p.Name + " can't be upgraded until the blocked checks pass"}
	}
	r, err := a.api.UpgradeProjectWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	op := r.JSON202
	if !a.noWait {
		if err := a.follow(op.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(op, func(w io.Writer) {
		fmt.Fprintf(w, "Upgraded %s to Postgres %d (operation %s); its URL and every password are unchanged.\n", p.Name, *to, op.Id)
	})
}

// move puts a project on another node (platform admin, V3 §2.3).
func (a *App) move(args []string) error {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	node := fs.String("node", "", "the id of the node to move to")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "move <project> --node <id>"); err != nil {
		return err
	}
	id, err := uuid.Parse(*node)
	if err != nil {
		return usageErrorf("--node takes the node's id")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.MoveProjectWithResponse(c, p.Id, client.MoveProjectRequest{NodeId: id})
	if err := check(r, err); err != nil {
		return err
	}
	op := r.JSON202
	if !a.noWait {
		if err := a.follow(op.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(op, func(w io.Writer) {
		fmt.Fprintf(w, "Moved %s (operation %s); its URL and every password are unchanged.\n", p.Name, op.Id)
	})
}

// moves lists a project's recent moves between instances.
func (a *App) moves(args []string) error {
	pos, err := parse(flag.NewFlagSet("moves", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "moves <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListProjectMovesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STARTED\tMODE\tPHASE\tWRITES PAUSED")
		for _, m := range r.JSON200.Items {
			paused := "-"
			if m.FreezeMs != nil {
				paused = strconv.FormatFloat(float64(*m.FreezeMs)/1000, 'f', 1, 64) + "s"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.StartedAt.Local().Format("2006-01-02 15:04"), m.Mode, m.Phase, paused)
		}
		_ = tw.Flush()
	})
}
