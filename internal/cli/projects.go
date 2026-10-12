package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/client"
)

func mustUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}

// orgID is the organisation commands act in: the token's.
func (a *App) orgID() (uuid.UUID, error) {
	if err := a.connectAPI(); err != nil {
		return uuid.Nil, err
	}
	if id, err := uuid.Parse(a.t.Org); err == nil {
		return id, nil
	}
	c, cancel := ctx()
	defer cancel()
	me, err := a.api.GetMeWithResponse(c)
	if err := check(me, err); err != nil {
		return uuid.Nil, err
	}
	if me.JSON200.Token == nil {
		return uuid.Nil, fmt.Errorf("not an API token")
	}
	a.t.Org = me.JSON200.Token.OrgId.String()
	return me.JSON200.Token.OrgId, nil
}

// project finds a project by id, name, or database name in the org.
func (a *App) project(ref string) (client.Project, error) {
	if err := a.connectAPI(); err != nil {
		return client.Project{}, err
	}
	c, cancel := ctx()
	defer cancel()
	if id, err := uuid.Parse(ref); err == nil {
		r, err := a.api.GetProjectWithResponse(c, id)
		if err := check(r, err); err != nil {
			return client.Project{}, err
		}
		return *r.JSON200, nil
	}
	r, err := a.api.ListProjectsWithResponse(c, &client.ListProjectsParams{})
	if err := check(r, err); err != nil {
		return client.Project{}, err
	}
	var found []client.Project
	for _, p := range r.JSON200.Items {
		if strings.EqualFold(p.Name, ref) || p.DbName == ref || p.Slug == ref {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return client.Project{}, &apiError{Status: 404, Code: "not_found", Message: fmt.Sprintf("no project %q in this organisation", ref)}
	case 1:
		return found[0], nil
	}
	return client.Project{}, usageErrorf("%d projects are called %q; use the project id", len(found), ref)
}

func (a *App) projectsList(args []string) error {
	if _, err := parse(flag.NewFlagSet("projects list", flag.ContinueOnError), args); err != nil {
		return err
	}
	if err := a.connectAPI(); err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListProjectsWithResponse(c, &client.ListProjectsParams{})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "NAME\tTIER\tSTATUS\tID\tCREATED")
		for _, p := range r.JSON200.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Tier, p.Status, p.Id, when(&p.CreatedAt))
		}
	})
}

func (a *App) projectsInfo(args []string) error {
	pos, err := parse(flag.NewFlagSet("projects info", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "projects info <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	return a.emit(p, func(w io.Writer) {
		fmt.Fprintf(w, "Name:\t%s\n", p.Name)
		fmt.Fprintf(w, "ID:\t%s\n", p.Id)
		fmt.Fprintf(w, "Tier:\t%s\n", p.Tier)
		fmt.Fprintf(w, "Status:\t%s\n", p.Status)
		if p.Lifecycle != nil && *p.Lifecycle != "active" {
			fmt.Fprintf(w, "Lifecycle:\t%s for inactivity (pgdock resume %s)\n", *p.Lifecycle, p.Name)
		}
		fmt.Fprintf(w, "Database:\t%s\n", p.DbName)
		if p.Region != nil {
			res := ""
			if p.DataResidency != nil && *p.DataResidency {
				res = " (data residency on)"
			}
			fmt.Fprintf(w, "Region:\t%s%s\n", *p.Region, res)
		}
		fmt.Fprintf(w, "Host:\t%s (pooled %d, session %d)\n", p.Connection.Host, p.Connection.PooledPort, p.Connection.SessionPort)
		if p.MyRole != nil {
			fmt.Fprintf(w, "Your role:\t%s\n", *p.MyRole)
		}
		fmt.Fprintf(w, "Last backup:\t%s\n", when(p.LastBackupAt))
		fmt.Fprintf(w, "Created:\t%s\n", when(&p.CreatedAt))
	})
}

func (a *App) projectsCreate(args []string) error {
	fs := flag.NewFlagSet("projects create", flag.ContinueOnError)
	tier := fs.String("tier", "shared", "shared or dedicated")
	profile := fs.String("profile", "", "dedicated size (small, medium, large)")
	desc := fs.String("description", "", "a description")
	version := fs.Int("pg-version", 0, "the Postgres major (default: the newest supported)")
	region := fs.String("region", "", "the region (see pgdock regions; default: the platform's home region)")
	residency := fs.Bool("data-residency", false, "keep the data, backups and branches in the region's country")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "projects create <name> [--tier shared|dedicated] [--profile …] [--pg-version 17] [--region ng-lagos [--data-residency]]"); err != nil {
		return err
	}
	org, err := a.orgID()
	if err != nil {
		return err
	}
	t := client.ProjectTier(*tier)
	req := client.CreateProjectRequest{Name: pos[0], OrgId: &org, Tier: &t}
	if *profile != "" {
		req.Profile = profile
	}
	if *desc != "" {
		req.Description = desc
	}
	if *version != 0 {
		req.PgVersion = version
	}
	if *region != "" {
		req.Region = region
	}
	if *residency {
		req.DataResidency = residency
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateProjectWithResponse(c, req)
	if err := check(r, err); err != nil {
		return err
	}
	cr := r.JSON202
	if !a.json {
		fmt.Fprintf(a.Stderr, "Creating %s (operation %s)…\n", cr.Project.Name, cr.Operation.Id)
	}
	if !a.noWait {
		if err := a.follow(cr.Operation.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(cr, func(w io.Writer) {
		fmt.Fprintf(w, "Project:\t%s (%s)\n", cr.Project.Name, cr.Project.Id)
		fmt.Fprintf(w, "Pooled URL:\t%s\n", cr.Connection.PooledUrl)
		fmt.Fprintf(w, "Session URL:\t%s\n", cr.Connection.SessionUrl)
		fmt.Fprintf(w, "Password:\t%s\n", cr.Password)
		fmt.Fprintln(w, "\nThe password is shown once: store it now.")
	})
}

func (a *App) projectsDelete(args []string) error {
	fs := flag.NewFlagSet("projects delete", flag.ContinueOnError)
	confirm := fs.String("confirm", "", "the project's name, typed, to confirm")
	skip := fs.Bool("skip-final-backup", false, "don't take a final backup")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "projects delete <project> --confirm <name>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	if *confirm != p.Name {
		return usageErrorf("deleting %s is permanent: add --confirm %q", p.Name, p.Name)
	}
	c, cancel := ctx()
	defer cancel()
	params := &client.DeleteProjectParams{Confirm: *confirm}
	if *skip {
		params.SkipFinalBackup = skip
	}
	r, err := a.api.DeleteProjectWithResponse(c, p.Id, params)
	if err := check(r, err); err != nil {
		return err
	}
	if r.JSON202 != nil && !a.noWait {
		if err := a.follow(r.JSON202.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(r.JSON202, func(w io.Writer) { fmt.Fprintf(w, "Deleted %s.\n", p.Name) })
}

// personalURLs returns this user's personal login for p, issuing (or
// rotating, with rotate) it when none is cached.
func (a *App) personalURLs(p client.Project, rotate bool) (credsEntry, *client.PersonalCredentials, error) {
	f, err := a.loadCreds()
	if err != nil {
		return credsEntry{}, nil, err
	}
	key := p.Id.String()
	if e, ok := f.Projects[key]; ok && !rotate && e.Server == a.t.Server {
		return e, nil, nil
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.IssueMyCredentialsWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return credsEntry{}, nil, err
	}
	cr := r.JSON200
	e := credsEntry{Server: a.t.Server, PooledURL: cr.Connection.PooledUrl, SessionURL: cr.Connection.SessionUrl}
	f.Projects[key] = e
	if err := a.saveCreds(f); err != nil {
		return e, cr, err
	}
	return e, cr, nil
}

func (a *App) connect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	session := fs.Bool("session", false, "the session-mode URL (port 5432) instead of transaction mode")
	pooled := fs.Bool("pooled", false, "the transaction-mode URL (the default)")
	psql := fs.Bool("psql", false, "open psql")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "connect <project> [--pooled|--session] [--psql]"); err != nil {
		return err
	}
	_ = pooled
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	e, _, err := a.personalURLs(p, false)
	if err != nil {
		return err
	}
	u := e.PooledURL
	if *session {
		u = e.SessionURL
	}
	if *psql {
		cmd := exec.Command("psql", u)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, a.Stdout, a.Stderr
		return cmd.Run()
	}
	return a.emit(map[string]string{"url": u}, func(w io.Writer) { fmt.Fprintln(w, u) })
}

func (a *App) creds(args []string) error {
	fs := flag.NewFlagSet("creds", flag.ContinueOnError)
	rotate := fs.Bool("rotate", false, "issue a new password (ends the old one)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "creds <project> [--rotate]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	if !*rotate {
		c, cancel := ctx()
		defer cancel()
		r, err := a.api.GetMyCredentialsWithResponse(c, p.Id)
		if err := check(r, err); err != nil {
			return err
		}
		f, _ := a.loadCreds()
		_, cached := f.Projects[p.Id.String()]
		return a.emit(r.JSON200, func(w io.Writer) {
			if !r.JSON200.Exists {
				fmt.Fprintf(w, "You have no personal login on %s yet: pgdock creds %s --rotate, or pgdock connect %s.\n", p.Name, pos[0], pos[0])
				return
			}
			fmt.Fprintf(w, "Login:\t%s\n", orDash(r.JSON200.Role))
			fmt.Fprintf(w, "Access:\t%s\n", r.JSON200.Access)
			if cached {
				fmt.Fprintf(w, "Saved locally:\tyes (pgdock connect %s prints the URL)\n", pos[0])
			} else {
				fmt.Fprintln(w, "Saved locally:\tno (the password was shown once; --rotate for a new one)")
			}
		})
	}
	e, cr, err := a.personalURLs(p, true)
	if err != nil {
		return err
	}
	return a.emit(cr, func(w io.Writer) {
		fmt.Fprintf(w, "Login:\t%s (%s)\n", cr.Role, cr.Access)
		fmt.Fprintf(w, "Password:\t%s\n", cr.Password)
		fmt.Fprintf(w, "Pooled URL:\t%s\n", e.PooledURL)
		fmt.Fprintf(w, "Session URL:\t%s\n", e.SessionURL)
	})
}

func (a *App) promote(args []string) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	node := fs.String("node", "", "the node's id (default: the least loaded)")
	profile := fs.String("profile", "", "the instance size")
	volume := fs.Int("volume-gb", 0, "the volume size in GB")
	reason := fs.String("reason", "", "why, if it becomes a request beyond your allowance")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "promote <project> [--node <id>] [--profile …]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.PromoteRequest{}
	if *node != "" {
		id, err := uuid.Parse(*node)
		if err != nil {
			return usageErrorf("--node takes the node's id")
		}
		req.NodeId = &id
	}
	if *profile != "" {
		req.Profile = profile
	}
	if *volume > 0 {
		req.VolumeGb = volume
	}
	if *reason != "" {
		req.Reason = reason
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.PromoteProjectWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	if r.JSON201 != nil {
		return a.emit(r.JSON201, func(w io.Writer) {
			fmt.Fprintf(w, "That is beyond your organisation's dedicated allowance: request %s is waiting for the platform admin.\n", r.JSON201.Id)
		})
	}
	op := r.JSON202
	if !a.noWait {
		if err := a.follow(op.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(op, func(w io.Writer) { fmt.Fprintf(w, "Promoted %s (operation %s).\n", p.Name, op.Id) })
}

// printChecks writes a demotion preflight as a checklist.
func printChecks(w io.Writer, pf *client.DemotePreflight) {
	mark := map[client.DemoteCheckStatus]string{client.DemoteCheckStatusOk: "ok", client.DemoteCheckStatusWarning: "WARN", client.DemoteCheckStatusBlocked: "BLOCKED"}
	for _, c := range pf.Checks {
		fmt.Fprintf(w, "  %-7s %-11s %s\n", mark[c.Status], c.Name, c.Message)
	}
	fmt.Fprintf(w, "Estimated write freeze: %ds for %s.\n", pf.EstimatedDowntimeSeconds, humanBytes(pf.SizeBytes))
	if len(pf.Resets) > 0 {
		fmt.Fprintf(w, "Guardrails reset to the shared defaults: %s.\n", strings.Join(pf.Resets, ", "))
	}
	fmt.Fprintf(w, "Point-in-time recovery ends at the demotion; existing base backups stay restorable until their retention ends.\n")
	fmt.Fprintf(w, "The dedicated instance is stopped and kept for %dh, then destroyed.\n", pf.RetainHours)
}

func (a *App) demote(args []string) error {
	fs := flag.NewFlagSet("demote", flag.ContinueOnError)
	node := fs.String("node", "", "the id of the node whose shared cluster takes it (default: the most free capacity)")
	checkOnly := fs.Bool("check", false, "only run the eligibility checks")
	accept := fs.Bool("accept-warnings", false, "go ahead despite the checks' warnings")
	writable := fs.Bool("console-writable", false, "turn the read-only SQL console off")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "demote <project> [--node <id>] [--check] [--accept-warnings] [--console-writable]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.DemoteRequest{}
	if *node != "" {
		id, err := uuid.Parse(*node)
		if err != nil {
			return usageErrorf("--node takes the node's id")
		}
		req.NodeId = &id
	}
	if *writable {
		req.ConsoleWritable = writable
	}
	c, cancel := ctx()
	defer cancel()
	pr, err := a.api.DemotePreflightWithResponse(c, p.Id, req)
	if err := check(pr, err); err != nil {
		return err
	}
	pf := pr.JSON200
	if *checkOnly {
		return a.emit(pf, func(w io.Writer) {
			fmt.Fprintf(w, "Demoting %s to the shared tier:\n", p.Name)
			printChecks(w, pf)
		})
	}
	if !a.json {
		fmt.Fprintf(a.Stderr, "Demoting %s to the shared tier:\n", p.Name)
		printChecks(a.Stderr, pf)
	}
	var warned bool
	for _, ch := range pf.Checks {
		warned = warned || ch.Status == client.DemoteCheckStatusWarning
	}
	switch {
	case !pf.Eligible:
		return &apiError{Status: 409, Code: "conflict", Message: p.Name + " can't be demoted until the blocked checks pass"}
	case warned && !*accept:
		return usageErrorf("review the warnings above, then run again with --accept-warnings")
	}
	if *accept {
		req.AcceptWarnings = accept
	}
	r, err := a.api.DemoteProjectWithResponse(c, p.Id, req)
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
		fmt.Fprintf(w, "Demoted %s to the shared tier (operation %s); its URL and every password are unchanged.\n", p.Name, op.Id)
	})
}

func (a *App) resume(args []string) error {
	pos, err := parse(flag.NewFlagSet("resume", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "resume <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ResumeProjectWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.followOp(r.JSON202, p.Name+" accepts connections again")
}

func (a *App) regionsList(args []string) error {
	if _, err := parse(flag.NewFlagSet("regions", flag.ContinueOnError), args); err != nil {
		return err
	}
	if err := a.connectAPI(); err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListRegionsWithResponse(c)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "ID\tNAME\tCOUNTRY\tRESIDENCY")
		for _, g := range r.JSON200.Items {
			name := g.Name
			if g.Home {
				name += " (default)"
			}
			res := "no"
			if g.Residency {
				res = "offered"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", g.Id, name, g.Country, res)
		}
	})
}
