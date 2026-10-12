package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// Backend services (V4 §2, §8.2): turning them on and off, and API keys.

func printServices(w io.Writer, name string, s *client.BackendServices) {
	if !s.Enabled {
		fmt.Fprintf(w, "Backend services for %s: off (pgdock services enable %s)\n", name, name)
		return
	}
	url := ""
	if s.Url != nil {
		url = *s.Url
	}
	fmt.Fprintf(w, "Backend services for %s: on\nAPI URL: %s\n", name, url)
	if e := s.Effective; e != nil {
		capped := func(n int, plan *int) string {
			if plan != nil && *plan <= n {
				return fmt.Sprintf("%d (the plan's most)", n)
			}
			return fmt.Sprint(n)
		}
		fmt.Fprintf(w, "In effect: statement timeout %s ms, %s requests/min per IP, %s per key\n",
			capped(e.StatementTimeoutMs, e.PlanTimeoutMs), capped(e.RatePerIp, e.PlanRatePerIp), capped(e.RatePerKey, e.PlanRatePerKey))
		if e.RequestsBlocked {
			fmt.Fprintln(w, "This month's data API requests are used up: data, storage and realtime answer 429 until the month ends")
		}
		if e.MauBlocked {
			fmt.Fprintln(w, "Monthly active users reached: new users can't sign in until the month ends")
		}
	}
	printKeys(w, s.Keys)
}

func printKeys(w io.Writer, keys []client.ApiKey) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tKIND\tKEY\tLAST USED\tSTATE")
	for _, k := range keys {
		shown, used, state := k.Prefix+"…", "never", "live"
		if k.Key != nil {
			shown = *k.Key
		}
		if k.LastUsedAt != nil {
			used = k.LastUsedAt.Local().Format("2006-01-02 15:04")
		}
		if k.RevokedAt != nil {
			state = "revoked"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.Id, k.Name, k.Kind, shown, used, state)
	}
	_ = tw.Flush()
}

func (a *App) servicesStatus(args []string) error {
	pos, err := parse(flag.NewFlagSet("services status", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "services status <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetBackendServicesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) { printServices(w, p.Name, r.JSON200) })
}

func (a *App) servicesEnable(args []string) error {
	pos, err := parse(flag.NewFlagSet("services enable", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "services enable <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.EnableBackendServicesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	if a.json {
		return a.emit(r.JSON202, nil)
	}
	for _, k := range r.JSON202.Keys {
		note := ""
		if k.Key.Kind == client.ApiKeyKindSecret {
			note = "  (shown once: store it now; servers only)"
		}
		fmt.Fprintf(a.Stdout, "%s key: %s%s\n", k.Key.Kind, k.Value, note)
	}
	op := r.JSON202.Operation
	return a.followOp(&op, "backend services are on for "+p.Name)
}

func (a *App) servicesDisable(args []string) error {
	pos, err := parse(flag.NewFlagSet("services disable", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "services disable <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.DisableBackendServicesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.followOp(r.JSON202, "backend services are off for "+p.Name+"; its keys are revoked, its pgd_* schemas kept")
}

func (a *App) keysList(args []string) error {
	pos, err := parse(flag.NewFlagSet("keys list", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "keys list <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetBackendServicesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Keys, func(w io.Writer) { printKeys(w, r.JSON200.Keys) })
}

func (a *App) keysCreate(args []string) error {
	fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
	kind := fs.String("kind", "publishable", "publishable (apps) or secret (servers)")
	name := fs.String("name", "", "a name for the key")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "keys create <project> --name <name> [--kind publishable|secret]"); err != nil {
		return err
	}
	if *name == "" {
		return usageErrorf("--name is required")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateAPIKeyWithResponse(c, p.Id, client.CreateApiKeyRequest{Kind: client.CreateApiKeyRequestKind(*kind), Name: *name})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON201, func(w io.Writer) {
		fmt.Fprintln(w, r.JSON201.Value)
		if r.JSON201.Key.Kind == client.ApiKeyKindSecret {
			fmt.Fprintln(a.Stderr, "This secret key is shown once: store it now, and only on servers.")
		}
	})
}

func (a *App) keysRevoke(args []string) error {
	pos, err := parse(flag.NewFlagSet("keys revoke", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "keys revoke <project> <key id>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	id, err := uuid.Parse(pos[1])
	if err != nil {
		return usageErrorf("the key id is a UUID (pgdock keys list %s)", pos[0])
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.RevokeAPIKeyWithResponse(c, p.Id, id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		fmt.Fprintf(w, "revoked %s (%s); the edge refuses it within seconds\n", r.JSON200.Name, r.JSON200.Prefix)
	})
}

func (a *App) genTypes(args []string) error {
	fs := flag.NewFlagSet("gen types", flag.ContinueOnError)
	lang := fs.String("lang", "ts", "ts, dart or go")
	pkg := fs.String("package", "", "Go's package name (default pgdtypes)")
	out := fs.String("o", "", "write to this file instead of standard output")
	project := fs.String("project", "", "the project (or as the first argument)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	name := *project
	if name == "" && len(pos) > 0 {
		name = pos[0]
	}
	if name == "" {
		return usageErrorf("gen types --lang ts|dart|go --project <p> [-o file]")
	}
	p, err := a.project(name)
	if err != nil {
		return err
	}
	params := &client.GetServiceTypesParams{Lang: client.GetServiceTypesParamsLang(*lang)}
	if *pkg != "" {
		params.Package = pkg
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetServiceTypesWithResponse(c, p.Id, params)
	if err := check(r, err); err != nil {
		return err
	}
	if *out == "" {
		_, err := a.Stdout.Write(r.Body)
		return err
	}
	if err := os.WriteFile(*out, r.Body, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.Stderr, "wrote %s (%d bytes)\n", *out, len(r.Body))
	return nil
}
