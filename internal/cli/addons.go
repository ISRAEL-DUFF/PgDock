package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// Billing add-ons (V4.1 §4): longer backup retention and a longer
// point-in-time recovery window.

func (a *App) backupRetention(args []string) error {
	pos, err := parse(flag.NewFlagSet("backup retention", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "backup retention <project> standard|extended|long"); err != nil {
		return err
	}
	ret := client.BackupRetention(pos[1])
	if !ret.Valid() {
		return fmt.Errorf("retention is standard, extended or long, not %q", pos[1])
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateProjectWithResponse(c, p.Id, client.UpdateProjectJSONRequestBody{Settings: &client.ProjectSettingsPatch{BackupRetention: &ret}})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Project.Settings, func(w io.Writer) {
		fmt.Fprintf(w, "%s keeps nightly backups on %s retention\n", p.Name, ret)
	})
}

func (a *App) pitrWindow(args []string) error {
	pos, err := parse(flag.NewFlagSet("pitr window", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "pitr window <project> 7|14|30"); err != nil {
		return err
	}
	n, err := strconv.Atoi(pos[1])
	days := client.InstanceUpdatePitrDays(n)
	if err != nil || !days.Valid() {
		return fmt.Errorf("the window is 7, 14 or 30 days, not %q", pos[1])
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateProjectInstanceWithResponse(c, p.Id, client.UpdateProjectInstanceJSONRequestBody{PitrDays: &days})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		fmt.Fprintf(w, "%s keeps %d days of point-in-time recovery", p.Name, n)
		if n > 7 {
			fmt.Fprint(w, " (the window grows to it day by day)")
		}
		fmt.Fprintln(w)
	})
}
