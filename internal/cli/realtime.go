package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// A project's realtime (V4 §6, §8.2): which tables' changes are delivered.

func (a *App) realtimeStatus(args []string) error {
	pos, err := parse(flag.NewFlagSet("realtime status", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "realtime status <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetProjectRealtimeWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		o := r.JSON200
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "TABLE\tREALTIME\tROW-LEVEL SECURITY")
		for _, t := range o.Tables {
			on, rls := "off", "off"
			if t.Enabled {
				on = "on"
			}
			if !t.HasPrimaryKey {
				on = "no primary key"
			}
			if t.Rls {
				rls = "on"
			}
			fmt.Fprintf(tw, "%s.%s\t%s\t%s\n", t.Schema, t.Table, on, rls)
		}
		_ = tw.Flush()
		limit := "unlimited"
		if o.MaxConnections != nil {
			limit = fmt.Sprint(*o.MaxConnections)
		}
		fmt.Fprintf(w, "Connections per edge: %s. This month: %.0f messages, %.0f connection-minutes", limit, o.MessagesThisMonth, o.ConnectionMinutesThisMonth)
		if o.MessagesBlocked {
			fmt.Fprint(w, " (the month's messages are used up)")
		}
		fmt.Fprintln(w)
		if len(o.PersistedTopics) > 0 {
			fmt.Fprintf(w, "History kept for: %s\n", strings.Join(o.PersistedTopics, ", "))
		}
	})
}

func (a *App) realtimeEnable(args []string) error  { return a.realtimeSet(args, true) }
func (a *App) realtimeDisable(args []string) error { return a.realtimeSet(args, false) }

func (a *App) realtimeSet(args []string, on bool) error {
	verb := map[bool]string{true: "enable", false: "disable"}[on]
	pos, err := parse(flag.NewFlagSet("realtime "+verb, flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "realtime "+verb+" <project> <[schema.]table>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	schema, table, ok := strings.Cut(pos[1], ".")
	if !ok {
		schema, table = "public", pos[1]
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.SetRealtimeTableWithResponse(c, p.Id, schema, table, client.SetRealtimeTableJSONRequestBody{Enabled: on})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(map[string]any{"table": schema + "." + table, "enabled": on}, func(w io.Writer) {
		fmt.Fprintf(w, "Realtime %sd for %s.%s\n", verb, schema, table)
	})
}

func (a *App) realtimeHistory(args []string) error {
	pos, err := parse(flag.NewFlagSet("realtime history", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usageErrorf("usage: pgdock realtime history <project> [topic…]  (no topics: keep none)")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	topics := append([]string{}, pos[1:]...)
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.SetRealtimePersistedTopicsWithResponse(c, p.Id, client.SetRealtimePersistedTopicsJSONRequestBody{Topics: topics})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(map[string]any{"persisted_topics": topics}, func(w io.Writer) {
		if len(topics) == 0 {
			fmt.Fprintln(w, "No broadcast history is kept")
			return
		}
		fmt.Fprintf(w, "Broadcasts on %s are kept 7 days\n", strings.Join(topics, ", "))
	})
}
