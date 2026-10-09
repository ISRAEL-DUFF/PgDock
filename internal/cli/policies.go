package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// Row-level security from the command line (V4.1 §9.4): the policies per
// table, the security advisor for CI, and the API's request log.

func (a *App) policiesList(args []string) error {
	fs := flag.NewFlagSet("policies list", flag.ContinueOnError)
	table := fs.String("table", "", "only this table ([schema.]table)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "policies list <project> [--table t]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetServicesCatalogWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	var tables []client.CatalogTable
	for _, t := range r.JSON200.Tables {
		if t.Kind != client.CatalogTableKindTable {
			continue
		}
		if *table != "" && *table != t.Name && *table != t.Schema+"."+t.Name {
			continue
		}
		tables = append(tables, t)
	}
	if *table != "" && len(tables) == 0 {
		return fmt.Errorf("no exposed table %s", *table)
	}
	return a.emit(tables, func(w io.Writer) {
		for i, t := range tables {
			if i > 0 {
				fmt.Fprintln(w)
			}
			rls := "row-level security on"
			if !t.Rls {
				rls = "row-level security OFF"
			}
			fmt.Fprintf(w, "%s.%s (%s)\n", t.Schema, t.Name, rls)
			if len(t.Policies) == 0 {
				fmt.Fprintln(w, "  no policies")
				continue
			}
			fmt.Fprintln(w, "  POLICY\tCOMMAND\tROLES\tUSING\tWITH CHECK")
			for _, pol := range t.Policies {
				kind := ""
				if !pol.Permissive {
					kind = " (restrictive)"
				}
				fmt.Fprintf(w, "  %s%s\t%s\t%s\t%s\t%s\n", pol.Name, kind, pol.Command, strings.Join(pol.Roles, ", "), orDash(pol.Using), orDash(pol.Check))
			}
		}
	})
}

// errDanger fails `policies lint` (exit 1) when the advisor finds danger.
var errDanger = errors.New("the security advisor found problems marked danger")

func (a *App) policiesLint(args []string) error {
	pos, err := parse(flag.NewFlagSet("policies lint", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "policies lint <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetSecurityAdvisorWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	danger := 0
	for _, f := range r.JSON200.Items {
		if f.Level == client.AdvisorFindingLevelDanger {
			danger++
		}
	}
	if err := a.emit(r.JSON200.Items, func(w io.Writer) {
		if len(r.JSON200.Items) == 0 {
			fmt.Fprintf(w, "%s: nothing to report\n", p.Name)
			return
		}
		fmt.Fprintln(w, "LEVEL\tOBJECT\tFINDING")
		for _, f := range r.JSON200.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\n", f.Level, f.Object, f.Message)
			if f.Fix != nil {
				fmt.Fprintf(w, "\t\tfix: %s\n", *f.Fix)
			}
		}
	}); err != nil {
		return err
	}
	if danger > 0 {
		return fmt.Errorf("%w (%d)", errDanger, danger)
	}
	return nil
}

func (a *App) logsAPI(args []string) error {
	fs := flag.NewFlagSet("logs api", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "keep printing new requests")
	status := fs.String("status", "", "a status (404) or class (5xx)")
	path := fs.String("path", "", "only paths starting with this (/data/v1/…)")
	limit := fs.Int("limit", 50, "how many recent requests to show first")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "logs api <project> [--follow] [--status 5xx] [--path /data/v1/…]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	params := client.ListAPIRequestLogsParams{Limit: limit}
	if *status != "" {
		params.Status = status
	}
	if *path != "" {
		params.Path = path
	}
	line := func(w io.Writer, l client.ApiRequestLog) {
		fmt.Fprintf(w, "%s  %d  %s %s  %dms  %s\n", l.At.Local().Format("2006-01-02 15:04:05"), l.Status, l.Method, l.Path, l.LatencyMs, orDash(l.Role))
	}
	c, cancel := ctx()
	r, err := a.api.ListAPIRequestLogsWithResponse(c, p.Id, &params)
	cancel()
	if err := check(r, err); err != nil {
		return err
	}
	items := r.JSON200.Items
	if !*follow {
		return a.emit(items, func(w io.Writer) {
			for i := len(items) - 1; i >= 0; i-- {
				line(w, items[i])
			}
		})
	}
	// Following: the recent ones oldest first, then whatever arrives.
	for i := len(items) - 1; i >= 0; i-- {
		if err := a.emitLine(items[i], line); err != nil {
			return err
		}
	}
	var after int64
	if len(items) > 0 {
		after = items[0].Id
	}
	wait := 25
	for {
		params := params
		params.After, params.Wait, params.Limit = &after, &wait, nil
		c, cancel := ctxFor(40 * time.Second)
		r, err := a.api.ListAPIRequestLogsWithResponse(c, p.Id, &params)
		cancel()
		if err := check(r, err); err != nil {
			return err
		}
		for _, l := range r.JSON200.Items {
			if err := a.emitLine(l, line); err != nil {
				return err
			}
		}
		if r.JSON200.Next != nil && *r.JSON200.Next > after {
			after = *r.JSON200.Next
		}
	}
}

// emitLine prints one log line, or one JSON object per line with --json.
func (a *App) emitLine(l client.ApiRequestLog, human func(io.Writer, client.ApiRequestLog)) error {
	if a.json {
		return writeJSONLine(a.Stdout, l)
	}
	human(a.Stdout, l)
	return nil
}

func ctxFor(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func writeJSONLine(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}
