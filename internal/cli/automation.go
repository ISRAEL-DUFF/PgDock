package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// listFlag collects a repeatable flag (--header K=V).
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func headers(l listFlag) (map[string]string, error) {
	h := map[string]string{}
	for _, kv := range l {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, usageErrorf("--header takes Name=value, got %q", kv)
		}
		h[strings.TrimSpace(k)] = v
	}
	return h, nil
}

// ---- Webhooks ----------------------------------------------------------------

func (a *App) webhooksOf(p client.Project) ([]client.Webhook, error) {
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListWebhooksWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return r.JSON200.Items, nil
}

// webhook finds a project's webhook by name or id.
func (a *App) webhook(projectRef, ref string) (client.Project, client.Webhook, error) {
	p, err := a.project(projectRef)
	if err != nil {
		return p, client.Webhook{}, err
	}
	hooks, err := a.webhooksOf(p)
	if err != nil {
		return p, client.Webhook{}, err
	}
	for _, w := range hooks {
		if w.Id.String() == ref || strings.EqualFold(w.Name, ref) {
			return p, w, nil
		}
	}
	return p, client.Webhook{}, &apiError{Status: 404, Code: "not_found", Message: fmt.Sprintf("%s has no webhook %q", p.Name, ref)}
}

func (a *App) webhooksList(args []string) error {
	pos, err := parse(flag.NewFlagSet("webhooks list", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "webhooks list <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	hooks, err := a.webhooksOf(p)
	if err != nil {
		return err
	}
	return a.emit(hooks, func(w io.Writer) {
		fmt.Fprintln(w, "NAME\tSTATUS\tTABLES\tEVENTS\tQUEUED\tURL")
		for _, h := range hooks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", h.Name, h.Status, strings.Join(h.Tables, ","), strings.Join(h.Events, ","), h.Backlog, h.Url)
		}
	})
}

func (a *App) webhooksCreate(args []string) error {
	fs := flag.NewFlagSet("webhooks create", flag.ContinueOnError)
	tables := fs.String("tables", "", "comma-separated tables (schema.table or table)")
	events := fs.String("events", "INSERT,UPDATE,DELETE", "comma-separated events")
	columns := fs.String("columns", "", "for UPDATE, fire only when one of these columns changed")
	url := fs.String("url", "", "the receiver's https:// URL")
	var hdrs listFlag
	fs.Var(&hdrs, "header", "a static header Name=value (repeatable; stored encrypted)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "webhooks create <project> <name> --tables orders --url https://… [--events INSERT,UPDATE] [--columns status] [--header K=V]"); err != nil {
		return err
	}
	if *tables == "" || *url == "" {
		return usageErrorf("--tables and --url are required")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.WebhookRequest{Name: pos[1], Tables: splitList(*tables), Url: *url}
	for _, e := range splitList(*events) {
		req.Events = append(req.Events, client.WebhookRequestEvents(strings.ToUpper(e)))
	}
	if cols := splitList(*columns); len(cols) > 0 {
		req.Columns = &cols
	}
	if len(hdrs) > 0 {
		h, err := headers(hdrs)
		if err != nil {
			return err
		}
		req.Headers = &h
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateWebhookWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	cr := r.JSON201
	return a.emit(cr, func(w io.Writer) {
		fmt.Fprintf(w, "Webhook:\t%s (%s)\n", cr.Webhook.Name, cr.Webhook.Id)
		fmt.Fprintf(w, "Signing secret:\t%s\n", cr.Secret)
		fmt.Fprintln(w, "\nThe secret is shown once: verify the PGDock-Signature header with it.")
	})
}

func (a *App) webhooksDelete(args []string) error {
	pos, err := parse(flag.NewFlagSet("webhooks delete", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "webhooks delete <project> <webhook>"); err != nil {
		return err
	}
	p, wh, err := a.webhook(pos[0], pos[1])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.DeleteWebhookWithResponse(c, p.Id, wh.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(map[string]any{"deleted": wh.Id}, func(w io.Writer) { fmt.Fprintf(w, "Deleted webhook %s.\n", wh.Name) })
}

func (a *App) webhooksDeliveries(args []string) error {
	fs := flag.NewFlagSet("webhooks deliveries", flag.ContinueOnError)
	dead := fs.Bool("dead", false, "only dead letters")
	limit := fs.Int("limit", 50, "how many")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "webhooks deliveries <project> <webhook> [--dead]"); err != nil {
		return err
	}
	p, wh, err := a.webhook(pos[0], pos[1])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListWebhookDeliveriesWithResponse(c, p.Id, wh.Id, &client.ListWebhookDeliveriesParams{Dead: dead, Limit: limit})
	if err := check(r, err); err != nil {
		return err
	}
	items := r.JSON200.Items
	return a.emit(items, func(w io.Writer) {
		fmt.Fprintln(w, "ID\tWHEN\tEVENT\tATTEMPT\tRESULT")
		for _, d := range items {
			res := "ok"
			switch {
			case d.DeadLettered:
				res = "dead letter"
			case !d.Succeeded:
				res = "failed"
			}
			if d.StatusCode != nil {
				res += " (HTTP " + strconv.Itoa(*d.StatusCode) + ")"
			} else if d.Error != nil {
				res += " (" + *d.Error + ")"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%s\n", d.Id, when(&d.CreatedAt), d.EventId, d.Attempt, res)
		}
	})
}

func (a *App) webhooksReplay(args []string) error {
	fs := flag.NewFlagSet("webhooks replay", flag.ContinueOnError)
	all := fs.Bool("all", false, "every dead letter")
	ids := fs.String("ids", "", "comma-separated delivery ids")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "webhooks replay <project> <webhook> --all | --ids 12,13"); err != nil {
		return err
	}
	req := client.ReplayRequest{}
	switch {
	case *all:
		req.All = all
	case *ids != "":
		var list []int64
		for _, s := range splitList(*ids) {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return usageErrorf("--ids takes delivery ids, got %q", s)
			}
			list = append(list, n)
		}
		req.Ids = &list
	default:
		return usageErrorf("say which dead letters: --all or --ids")
	}
	p, wh, err := a.webhook(pos[0], pos[1])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ReplayWebhookWithResponse(c, p.Id, wh.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		fmt.Fprintf(w, "Queued %d dead letter(s) of %s again.\n", r.JSON200.Queued, wh.Name)
	})
}

// ---- Scheduled jobs ----------------------------------------------------------

func (a *App) jobsOf(p client.Project) ([]client.Job, error) {
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListJobsWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return r.JSON200.Items, nil
}

func (a *App) job(projectRef, ref string) (client.Project, client.Job, error) {
	p, err := a.project(projectRef)
	if err != nil {
		return p, client.Job{}, err
	}
	jobs, err := a.jobsOf(p)
	if err != nil {
		return p, client.Job{}, err
	}
	for _, j := range jobs {
		if j.Id.String() == ref || strings.EqualFold(j.Name, ref) {
			return p, j, nil
		}
	}
	return p, client.Job{}, &apiError{Status: 404, Code: "not_found", Message: fmt.Sprintf("%s has no job %q", p.Name, ref)}
}

func (a *App) jobsList(args []string) error {
	pos, err := parse(flag.NewFlagSet("jobs list", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "jobs list <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	jobs, err := a.jobsOf(p)
	if err != nil {
		return err
	}
	return a.emit(jobs, func(w io.Writer) {
		fmt.Fprintln(w, "NAME\tKIND\tSCHEDULE\tNEXT RUN\tLAST RUN")
		for _, j := range jobs {
			next := "paused"
			if j.Enabled {
				next = when(j.NextRunAt)
			}
			last := "-"
			if j.LastRun != nil {
				last = string(j.LastRun.Status) + " " + when(&j.LastRun.ScheduledFor)
			}
			fmt.Fprintf(w, "%s\t%s\t%s (%s)\t%s\t%s\n", j.Name, j.Kind, j.Cron, j.Timezone, next, last)
		}
	})
}

func (a *App) jobsCreate(args []string) error {
	fs := flag.NewFlagSet("jobs create", flag.ContinueOnError)
	cron := fs.String("cron", "", "a 5-field cron expression, or @hourly, @daily, …")
	tz := fs.String("tz", "UTC", "the schedule's time zone (IANA)")
	sqlText := fs.String("sql", "", "the SQL to run as the project owner (or @file.sql)")
	url := fs.String("url", "", "the URL an HTTP job calls")
	method := fs.String("method", "POST", "the HTTP job's method")
	body := fs.String("body", "", "the HTTP job's body")
	timeout := fs.Int("timeout", 0, "seconds (default 300 for SQL, 30 for HTTP; at most 3600)")
	queue := fs.Bool("queue", false, "queue one run when the previous is still going (default: skip)")
	var hdrs listFlag
	fs.Var(&hdrs, "header", "an HTTP job header Name=value (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "jobs create <project> <name> --cron '0 3 * * *' (--sql '…' | --url https://…) [--tz Europe/Berlin]"); err != nil {
		return err
	}
	if *cron == "" || (*sqlText == "") == (*url == "") {
		return usageErrorf("--cron and one of --sql or --url are required")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.JobRequest{Name: pos[1], Cron: *cron, Timezone: tz}
	if *sqlText != "" {
		text := *sqlText
		if f, ok := strings.CutPrefix(text, "@"); ok {
			b, err := os.ReadFile(f)
			if err != nil {
				return usageErrorf("%v", err)
			}
			text = string(b)
		}
		req.Kind, req.Sql = client.JobRequestKindSql, &text
	} else {
		m := client.HttpJobSpecMethod(strings.ToUpper(*method))
		spec := client.HttpJobSpec{Url: *url, Method: &m}
		if *body != "" {
			spec.Body = body
		}
		if len(hdrs) > 0 {
			h, err := headers(hdrs)
			if err != nil {
				return err
			}
			spec.Headers = &h
		}
		req.Kind, req.Http = client.JobRequestKindHttp, &spec
	}
	if *timeout > 0 {
		req.TimeoutSeconds = timeout
	}
	if *queue {
		o := client.JobRequestOverlapQueue
		req.Overlap = &o
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateJobWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	cr := r.JSON201
	return a.emit(cr, func(w io.Writer) {
		fmt.Fprintf(w, "Job:\t%s (%s)\n", cr.Job.Name, cr.Job.Id)
		for i, t := range cr.Job.Upcoming {
			label := ""
			if i == 0 {
				label = "Next runs:"
			}
			fmt.Fprintf(w, "%s\t%s\n", label, when(&t))
		}
		if cr.Secret != nil {
			fmt.Fprintf(w, "Signing secret:\t%s\n\nThe secret is shown once: verify the PGDock-Signature header with it.\n", *cr.Secret)
		}
	})
}

func (a *App) jobsSetEnabled(args []string, enabled bool) error {
	name := "jobs resume"
	if !enabled {
		name = "jobs pause"
	}
	pos, err := parse(flag.NewFlagSet(name, flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, name+" <project> <job>"); err != nil {
		return err
	}
	p, j, err := a.job(pos[0], pos[1])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateJobWithResponse(c, p.Id, j.Id, client.JobUpdate{Enabled: &enabled})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		if enabled {
			fmt.Fprintf(w, "Resumed %s; next run %s.\n", j.Name, when(r.JSON200.NextRunAt))
		} else {
			fmt.Fprintf(w, "Paused %s.\n", j.Name)
		}
	})
}

func (a *App) jobsPause(args []string) error  { return a.jobsSetEnabled(args, false) }
func (a *App) jobsResume(args []string) error { return a.jobsSetEnabled(args, true) }

func (a *App) jobsRun(args []string) error {
	pos, err := parse(flag.NewFlagSet("jobs run", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "jobs run <project> <job>"); err != nil {
		return err
	}
	p, j, err := a.job(pos[0], pos[1])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.RunJobWithResponse(c, p.Id, j.Id)
	if err := check(r, err); err != nil {
		return err
	}
	run := r.JSON202
	return a.emit(run, func(w io.Writer) {
		fmt.Fprintf(w, "Run %d of %s: %s", run.Id, j.Name, run.Status)
		if run.Error != nil {
			fmt.Fprintf(w, " (%s)", *run.Error)
		}
		fmt.Fprintln(w, ". See `pgdock jobs history` for the outcome.")
	})
}

func (a *App) jobsHistory(args []string) error {
	fs := flag.NewFlagSet("jobs history", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "how many runs")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "jobs history <project> <job>"); err != nil {
		return err
	}
	p, j, err := a.job(pos[0], pos[1])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListJobRunsWithResponse(c, p.Id, j.Id, &client.ListJobRunsParams{Limit: limit})
	if err := check(r, err); err != nil {
		return err
	}
	runs := r.JSON200.Items
	return a.emit(runs, func(w io.Writer) {
		fmt.Fprintln(w, "SCHEDULED\tTRIGGER\tSTATUS\tDURATION\tRESULT")
		for _, run := range runs {
			dur := "-"
			if run.StartedAt != nil && run.FinishedAt != nil {
				dur = run.FinishedAt.Sub(*run.StartedAt).Round(1e6).String()
			}
			res := ""
			switch {
			case run.Error != nil:
				res = *run.Error
			case run.RowsAffected != nil:
				res = fmt.Sprintf("%d row(s)", *run.RowsAffected)
			case run.StatusCode != nil:
				res = fmt.Sprintf("HTTP %d", *run.StatusCode)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", when(&run.ScheduledFor), run.Trigger, run.Status, dur, res)
		}
	})
}
