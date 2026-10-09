package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// parseTTL reads "72h", "7d", "90m", or "0" (keep until deleted).
func parseTTL(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("bad TTL %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad TTL %q (use e.g. 72h or 7d, or 0 to keep it)", s)
	}
	return d, nil
}

// branch finds a branch by id, name, or "<parent>/<name>".
func (a *App) branch(ref string) (client.Project, error) {
	if parent, name, ok := strings.Cut(ref, "/"); ok {
		p, err := a.project(parent)
		if err != nil {
			return client.Project{}, err
		}
		bs, err := a.branchesOf(p)
		if err != nil {
			return client.Project{}, err
		}
		for _, b := range bs {
			if strings.EqualFold(b.Name, name) {
				return b, nil
			}
		}
		return client.Project{}, &apiError{Status: 404, Code: "not_found", Message: fmt.Sprintf("%s has no branch %q", p.Name, name)}
	}
	b, err := a.project(ref)
	if err != nil {
		return b, err
	}
	if b.ParentProjectId == nil {
		return b, usageErrorf("%s is not a branch", b.Name)
	}
	return b, nil
}

func (a *App) branchesOf(p client.Project) ([]client.Project, error) {
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListBranchesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return r.JSON200.Items, nil
}

func expiry(b client.Project) string {
	if b.Branch == nil || b.Branch.ExpiresAt == nil {
		return "never"
	}
	return when(b.Branch.ExpiresAt)
}

func (a *App) branchList(args []string) error {
	pos, err := parse(flag.NewFlagSet("branch list", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "branch list <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	bs, err := a.branchesOf(p)
	if err != nil {
		return err
	}
	return a.emit(bs, func(w io.Writer) {
		fmt.Fprintln(w, "NAME\tSTATUS\tSOURCE\tEXPIRES\tID")
		for _, b := range bs {
			src := ""
			if b.Branch != nil {
				src = string(b.Branch.Source)
				if b.Branch.SchemaOnly {
					src += " (schema only)"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", b.Name, b.Status, src, expiry(b), b.Id)
		}
	})
}

// envLines are the --env output: KEY=value lines for $GITHUB_ENV or .env.
func envLines(w io.Writer, cr *client.ProjectCredentials) {
	fmt.Fprintf(w, "DATABASE_URL=%s\n", cr.Connection.PooledUrl)
	fmt.Fprintf(w, "DATABASE_URL_SESSION=%s\n", cr.Connection.SessionUrl)
	fmt.Fprintf(w, "PGDOCK_BRANCH_ID=%s\n", cr.Project.Id)
	fmt.Fprintf(w, "PGDOCK_BRANCH=%s\n", cr.Project.Name)
	if api := cr.Api; api != nil {
		if api.Url != nil {
			fmt.Fprintf(w, "PGDOCK_API_URL=%s\n", *api.Url)
		}
		fmt.Fprintf(w, "PGDOCK_PUBLISHABLE_KEY=%s\n", api.PublishableKey)
		fmt.Fprintf(w, "PGDOCK_SECRET_KEY=%s\n", api.SecretKey)
	}
}

func (a *App) branchCreate(args []string) error {
	fs := flag.NewFlagSet("branch create", flag.ContinueOnError)
	from := fs.String("from", "backup", "where the data comes from: backup (the parent's latest) or live")
	schemaOnly := fs.Bool("schema-only", false, "copy the schema without data")
	withData := fs.Bool("with-data", false, "copy the data too (the default unless the parent contains sensitive data)")
	ttl := fs.String("ttl", "7d", "delete the branch after this long, e.g. 72h or 7d; 0 keeps it")
	env := fs.Bool("env", false, "print DATABASE_URL=… lines (for $GITHUB_ENV or a .env file)")
	replace := fs.Bool("replace", false, "delete an existing branch of the same name first (one branch per pull request)")
	copyFiles := fs.Bool("copy-files", false, "with backend services: copy the parent's stored files too, in the background")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "branch create <project> <name> [--from backup|live] [--schema-only] [--ttl 72h] [--env] [--copy-files]"); err != nil {
		return err
	}
	if *schemaOnly && *withData {
		return usageErrorf("--schema-only and --with-data contradict each other")
	}
	d, err := parseTTL(*ttl)
	if err != nil {
		return usageErrorf("%v", err)
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	name := pos[1]
	if *replace {
		bs, err := a.branchesOf(p)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if strings.EqualFold(b.Name, name) {
				if err := a.deleteBranch(b); err != nil {
					return err
				}
			}
		}
	}
	src := client.BranchRequestSource(*from)
	hours := int(d / time.Hour)
	if d > 0 && hours == 0 {
		return usageErrorf("the TTL must be at least 1h")
	}
	req := client.BranchRequest{Name: name, Source: &src, TtlHours: &hours}
	if *copyFiles {
		req.CopyFiles = copyFiles
	}
	switch {
	case *schemaOnly:
		req.SchemaOnly = schemaOnly
	case *withData:
		f := false
		req.SchemaOnly = &f
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateBranchWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	cr := r.JSON202
	if !a.json {
		fmt.Fprintf(a.Stderr, "Branching %s into %s (operation %s)…\n", p.Name, cr.Project.Name, cr.Operation.Id)
	}
	if !a.noWait {
		if err := a.follow(cr.Operation.Id, !a.json && !*env); err != nil {
			return err
		}
	}
	if *env {
		envLines(a.Stdout, cr)
		return nil
	}
	return a.emit(cr, func(w io.Writer) {
		fmt.Fprintf(w, "Branch:\t%s (%s) of %s\n", cr.Project.Name, cr.Project.Id, p.Name)
		if cr.Project.Branch != nil && cr.Project.Branch.ExpiresAt != nil {
			fmt.Fprintf(w, "Expires:\t%s\n", when(cr.Project.Branch.ExpiresAt))
		}
		fmt.Fprintf(w, "Pooled URL:\t%s\n", cr.Connection.PooledUrl)
		fmt.Fprintf(w, "Session URL:\t%s\n", cr.Connection.SessionUrl)
		fmt.Fprintf(w, "Password:\t%s\n", cr.Password)
		if api := cr.Api; api != nil {
			fmt.Fprintf(w, "API URL:\t%s\n", orDash(api.Url))
			fmt.Fprintf(w, "Publishable key:\t%s\n", api.PublishableKey)
			fmt.Fprintf(w, "Secret key:\t%s\n", api.SecretKey)
		}
		fmt.Fprintln(w, "\nThe password and keys are shown once: store them now. Resets keep them.")
	})
}

func (a *App) deleteBranch(b client.Project) error {
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.DeleteProjectWithResponse(c, b.Id, &client.DeleteProjectParams{Confirm: b.Name})
	if err := check(r, err); err != nil {
		return err
	}
	if !a.json {
		fmt.Fprintf(a.Stderr, "Deleting branch %s…\n", b.Name)
	}
	if r.JSON202 != nil {
		return a.follow(r.JSON202.Id, false)
	}
	return nil
}

func (a *App) branchReset(args []string) error {
	fs := flag.NewFlagSet("branch reset", flag.ContinueOnError)
	from := fs.String("from", "", "backup or live (default: how the branch was made)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "branch reset <branch> [--from backup|live]"); err != nil {
		return err
	}
	b, err := a.branch(pos[0])
	if err != nil {
		return err
	}
	req := client.BranchResetRequest{}
	if *from != "" {
		s := client.BranchResetRequestSource(*from)
		req.Source = &s
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ResetBranchWithResponse(c, b.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	if !a.json {
		fmt.Fprintf(a.Stderr, "Resetting %s from its parent (operation %s); its URL and password stay the same…\n", b.Name, r.JSON202.Id)
	}
	if !a.noWait {
		if err := a.follow(r.JSON202.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(r.JSON202, func(w io.Writer) { fmt.Fprintf(w, "Reset %s.\n", b.Name) })
}

func (a *App) branchDelete(args []string) error {
	fs := flag.NewFlagSet("branch delete", flag.ContinueOnError)
	confirm := fs.String("confirm", "", "the branch's name, typed, to confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "branch delete <branch> --confirm <name>"); err != nil {
		return err
	}
	b, err := a.branch(pos[0])
	if err != nil {
		return err
	}
	if *confirm != b.Name {
		return usageErrorf("deleting branch %s is permanent: add --confirm %q", b.Name, b.Name)
	}
	if err := a.deleteBranch(b); err != nil {
		return err
	}
	return a.emit(map[string]any{"deleted": b.Id}, func(w io.Writer) { fmt.Fprintf(w, "Deleted branch %s.\n", b.Name) })
}

func (a *App) branchExtend(args []string) error {
	fs := flag.NewFlagSet("branch extend", flag.ContinueOnError)
	ttl := fs.String("ttl", "7d", "keep it this much longer from now, e.g. 48h or 7d; 0 keeps it until deleted")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "branch extend <branch> [--ttl 7d]"); err != nil {
		return err
	}
	d, err := parseTTL(*ttl)
	if err != nil {
		return usageErrorf("%v", err)
	}
	b, err := a.branch(pos[0])
	if err != nil {
		return err
	}
	req := client.UpdateProjectRequest{}
	if d == 0 {
		keep := true
		req.NoExpiry = &keep
	} else {
		at := time.Now().Add(d).UTC()
		req.ExpiresAt = &at
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateProjectWithResponse(c, b.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	np := r.JSON200.Project
	return a.emit(np, func(w io.Writer) { fmt.Fprintf(w, "%s now expires: %s\n", np.Name, expiry(np)) })
}

func (a *App) branchDetach(args []string) error {
	pos, err := parse(flag.NewFlagSet("branch detach", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "branch detach <branch>"); err != nil {
		return err
	}
	b, err := a.branch(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.DetachBranchWithResponse(c, b.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		fmt.Fprintf(w, "%s is now a standalone project; it no longer expires.\n", r.JSON200.Name)
	})
}
