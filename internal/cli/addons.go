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

// Resizing a dedicated instance (V4.1 §5).

func (a *App) instanceResize(args []string) error {
	fs := flag.NewFlagSet("instance resize", flag.ContinueOnError)
	profile := fs.String("profile", "", "a size from the profile list (small, medium, large)")
	cpus := fs.Float64("cpus", 0, "vCPUs")
	memory := fs.Int("memory", 0, "memory in MB")
	dry := fs.Bool("dry-run", false, "only say what the resize would do")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "instance resize <project> (--profile <name> | --cpus N --memory MB)"); err != nil {
		return err
	}
	req := client.InstanceUpdate{DryRun: dry}
	if *profile != "" {
		req.Profile = profile
	}
	if *cpus > 0 {
		v := float32(*cpus)
		req.Cpus = &v
	}
	if *memory > 0 {
		req.MemoryMb = memory
	}
	if req.Profile == nil && req.Cpus == nil && req.MemoryMb == nil {
		return fmt.Errorf("give --profile, or --cpus and/or --memory")
	}
	return a.updateInstance(pos[0], req)
}

func (a *App) instanceDisk(args []string) error {
	fs := flag.NewFlagSet("instance disk", flag.ContinueOnError)
	gb := fs.Int("gb", 0, "the new disk size in GB (it only grows)")
	dry := fs.Bool("dry-run", false, "only say what it would do")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "instance disk <project> --gb N"); err != nil {
		return err
	}
	if *gb <= 0 {
		return fmt.Errorf("give the new size with --gb")
	}
	return a.updateInstance(pos[0], client.InstanceUpdate{DiskGb: gb, DryRun: dry})
}

func (a *App) updateInstance(project string, req client.InstanceUpdate) error {
	p, err := a.project(project)
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.UpdateProjectInstanceWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	out := r.JSON200
	if a.json || out.Plan == nil {
		return a.emit(out, nil)
	}
	pl := out.Plan
	what := fmt.Sprintf("%g → %g vCPU, %d → %d MB, disk %d → %d GB", pl.From.Cpus, pl.To.Cpus, pl.From.MemoryMb, pl.To.MemoryMb, pl.From.DiskGb, pl.To.DiskGb)
	how := "in place, with a restart of a few seconds"
	switch {
	case pl.MoveTo != nil:
		how = "by moving to node " + *pl.MoveTo + " (its node has no room)"
	case !pl.Restart:
		how = "with no restart"
	}
	if out.Operation == nil {
		fmt.Fprintf(a.Stdout, "%s: %s, %s\n", p.Name, what, how)
		return nil
	}
	fmt.Fprintf(a.Stdout, "%s: %s, %s\n", p.Name, what, how)
	return a.followOp(out.Operation, p.Name+" resized")
}
