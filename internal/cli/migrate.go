package cli

import (
	"flag"
	"fmt"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// migrateSupabase runs the Supabase migration helper's steps (V4 §9) on a
// project whose database was imported from Supabase. The source's
// connection string and S3 secret come from flags or, to keep them out of
// shell history, PGDOCK_SUPABASE_DB_URL and PGDOCK_SUPABASE_S3_SECRET.
func (a *App) migrateSupabase(args []string) error {
	fs := flag.NewFlagSet("migrate supabase", flag.ContinueOnError)
	step := fs.String("step", "all", "policies, users, storage, or all (in that order)")
	source := fs.String("source", "", "the Supabase database's connection string (or PGDOCK_SUPABASE_DB_URL)")
	endpoint := fs.String("s3-endpoint", "", "the Supabase project's S3 endpoint, https://<ref>.supabase.co/storage/v1/s3")
	region := fs.String("s3-region", "", "the S3 region Supabase shows")
	key := fs.String("s3-access-key", "", "the S3 access key id")
	secret := fs.String("s3-secret-key", "", "the S3 secret (or PGDOCK_SUPABASE_S3_SECRET)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	const usage = "migrate supabase <project> [--step all|policies|users|storage] [--source <url>] [--s3-endpoint <url> --s3-access-key <id> --s3-secret-key <secret>]"
	if err := need(pos, 1, usage); err != nil {
		return err
	}
	if *source == "" {
		*source = a.getenv("PGDOCK_SUPABASE_DB_URL")
	}
	if *secret == "" {
		*secret = a.getenv("PGDOCK_SUPABASE_S3_SECRET")
	}
	var steps []string
	switch *step {
	case "all":
		steps = []string{"policies", "users"}
		if *endpoint != "" {
			steps = append(steps, "storage")
		}
	case "policies", "users", "storage":
		steps = []string{*step}
	default:
		return usageErrorf("--step is all, policies, users or storage")
	}
	for _, s := range steps {
		if s != "policies" && *source == "" {
			return usageErrorf("the %s step reads the Supabase database: pass --source or set PGDOCK_SUPABASE_DB_URL", s)
		}
		if s == "storage" && (*endpoint == "" || *key == "" || *secret == "") {
			return usageErrorf("the storage step needs --s3-endpoint, --s3-access-key and --s3-secret-key (Supabase: Project Settings → Storage → S3 connection)")
		}
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	done := map[string]string{
		"policies": "policies and defaults now use " + p.Name + "'s request roles and pgd_auth",
		"users":    "users copied; they sign in with their Supabase passwords",
		"storage":  "buckets, files and storage policies copied",
	}
	for _, s := range steps {
		req := client.SupabaseMigrationRequest{Step: client.SupabaseMigrationRequestStep(s)}
		if s != "policies" {
			req.SourceUrl = source
		}
		if s == "storage" {
			req.S3 = &client.SupabaseS3{Endpoint: *endpoint, AccessKey: *key, SecretKey: *secret}
			if *region != "" {
				req.S3.Region = region
			}
		}
		if !a.json {
			fmt.Fprintf(a.Stderr, "Step %s…\n", s)
		}
		c, cancel := ctx()
		r, err := a.api.MigrateSupabaseWithResponse(c, p.Id, req)
		cancel()
		if err := check(r, err); err != nil {
			return err
		}
		if err := a.followOp(r.JSON202, done[s]); err != nil {
			return err
		}
	}
	return nil
}
