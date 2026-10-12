package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/israel-duff/pgdock/internal/api/client"
)

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func (a *App) insightsQueries(args []string) error {
	fs := flag.NewFlagSet("insights queries", flag.ContinueOnError)
	rng := fs.String("range", "24h", "1h, 24h, 7d or 30d")
	sort := fs.String("sort", "total", "total, mean, calls or rows")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "insights queries <project> [--range 24h] [--sort total]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r2, s2 := client.ListInsightQueriesParamsRange(*rng), client.ListInsightQueriesParamsSort(*sort)
	r, err := a.api.ListInsightQueriesWithResponse(c, p.Id, &client.ListInsightQueriesParams{Range: &r2, Sort: &s2})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "QUERY ID\tCALLS\tTOTAL MS\tMEAN MS\tSHARE\tQUERY")
		for _, q := range r.JSON200.Items {
			fmt.Fprintf(w, "%s\t%d\t%.0f\t%.2f\t%.1f%%\t%s\n", q.QueryId, q.Calls, q.TotalMs, q.MeanMs, q.Share*100, oneLine(q.Query, 80))
		}
	})
}

func (a *App) insightsSlow(args []string) error {
	fs := flag.NewFlagSet("insights slow", flag.ContinueOnError)
	rng := fs.String("range", "24h", "1h, 24h, 7d or 30d")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "insights slow <project> [--range 24h]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r2 := client.ListSlowQueriesParamsRange(*rng)
	r, err := a.api.ListSlowQueriesWithResponse(c, p.Id, &client.ListSlowQueriesParams{Range: &r2})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "WHEN\tDURATION MS\tSOURCE\tQUERY")
		for _, q := range r.JSON200.Items {
			fmt.Fprintf(w, "%s\t%.0f\t%s\t%s\n", when(&q.SeenAt), q.DurationMs, q.Source, oneLine(q.Query, 80))
		}
	})
}

func (a *App) insightsIndexes(args []string) error {
	pos, err := parse(flag.NewFlagSet("insights indexes", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "insights indexes <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetInsightIndexesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	rep := r.JSON200
	return a.emit(rep, func(w io.Writer) {
		if len(rep.Suggestions) == 0 {
			fmt.Fprintln(w, "No index suggestions.")
		}
		for _, s := range rep.Suggestions {
			est := ""
			if s.Estimate != nil {
				est = fmt.Sprintf(" (estimated %.0f%% less cost)", s.Estimate.Improvement*100)
			}
			fmt.Fprintf(w, "Suggested:\t%s%s\n", s.Statement, est)
		}
		for _, i := range rep.Unused {
			fmt.Fprintf(w, "Unused:\t%s.%s (never scanned)\n", i.Schema, i.Name)
		}
		for _, d := range rep.Duplicates {
			fmt.Fprintf(w, "Duplicate:\t%s.%s (of %s)\n", d.Schema, d.Name, d.Of)
		}
	})
}
