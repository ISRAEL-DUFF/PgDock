package cli

import (
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/client"
)

func printHA(w io.Writer, name string, st *client.HAStatus) {
	state := "off"
	if st.Enabled {
		state = "on"
		if st.Synchronous {
			state = "on, synchronous replication"
		}
	}
	fmt.Fprintf(w, "HA for %s: %s\n", name, state)
	if len(st.Members) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "MEMBER\tROLE\tNODE\tSTATE\tBEHIND")
		for _, m := range st.Members {
			state, lag := "-", "-"
			if m.State != nil {
				state = *m.State
			}
			if m.LagBytes != nil && m.Role != client.HAMemberRoleLeader {
				lag = humanBytes(*m.LagBytes)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.Id, m.Role, m.NodeName, state, lag)
		}
		_ = tw.Flush()
	}
	if a := st.Availability; a != nil && a.Percent != nil {
		fmt.Fprintf(w, "Availability %s: %.3f%% (%d of %d minutes unavailable)\n", a.Month, *a.Percent, a.UnavailableMinutes, a.MeasuredMinutes)
	}
	for i, f := range st.Failovers {
		if i == 5 {
			break
		}
		from, to, took := "?", "?", "-"
		if f.FromNode != nil {
			from = *f.FromNode
		}
		if f.ToNode != nil {
			to = *f.ToNode
		}
		if f.DurationMs != nil {
			took = fmt.Sprintf("%.1fs", float64(*f.DurationMs)/1000)
		}
		fmt.Fprintf(w, "  %s %s: %s -> %s (%s)\n", f.OccurredAt.Local().Format("2006-01-02 15:04"), f.Kind, from, to, took)
	}
}

func (a *App) haStatus(args []string) error {
	pos, err := parse(flag.NewFlagSet("ha status", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "ha status <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetProjectHAWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) { printHA(w, p.Name, r.JSON200) })
}

func (a *App) haEnable(args []string) error {
	fs := flag.NewFlagSet("ha enable", flag.ContinueOnError)
	node := fs.String("node", "", "the id of the node for the standby (default: the least loaded)")
	sync := fs.Bool("sync", false, "synchronous replication: nothing committed is lost on failover")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "ha enable <project> [--node <id>] [--sync]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.HAEnableRequest{}
	if *node != "" {
		id, err := uuid.Parse(*node)
		if err != nil {
			return usageErrorf("--node takes the node's id")
		}
		req.NodeId = &id
	}
	if *sync {
		req.Synchronous = sync
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.EnableProjectHAWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	return a.followOp(r.JSON202, "HA is on for "+p.Name+"; the URL is unchanged")
}

func (a *App) haDisable(args []string) error {
	pos, err := parse(flag.NewFlagSet("ha disable", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "ha disable <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.DisableProjectHAWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.followOp(r.JSON202, "HA is off for "+p.Name+"; the standby was removed")
}

func (a *App) haSwitchover(args []string) error {
	fs := flag.NewFlagSet("ha switchover", flag.ContinueOnError)
	to := fs.String("to", "", "the member id to switch to (default: the most current standby)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "ha switchover <project> [--to <member>]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.SwitchoverRequest{}
	if *to != "" {
		id, err := uuid.Parse(*to)
		if err != nil {
			return usageErrorf("--to takes a member's id (see pgdock ha status)")
		}
		req.Candidate = &id
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.SwitchoverProjectWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	return a.followOp(r.JSON202, "switched over; the URL is unchanged")
}

func (a *App) haSync(args []string) error {
	pos, err := parse(flag.NewFlagSet("ha sync", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "ha sync <project> on|off"); err != nil {
		return err
	}
	on := pos[1] == "on"
	if !on && pos[1] != "off" {
		return usageErrorf("ha sync takes on or off")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateProjectHAWithResponse(c, p.Id, client.HAUpdateRequest{Synchronous: on})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) { printHA(w, p.Name, r.JSON200) })
}

// followOp streams an operation's progress unless --no-wait, then reports
// done.
func (a *App) followOp(op *client.Operation, done string) error {
	if !a.noWait {
		if err := a.follow(op.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(op, func(w io.Writer) { fmt.Fprintf(w, "%s (operation %s).\n", done, op.Id) })
}
