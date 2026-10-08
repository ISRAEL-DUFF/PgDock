package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/israel-duff/pgdock/internal/api/client"
)

// A project's files (V4 §5, §8.2). Remote paths are written ss:///bucket/path.

const remotePrefix = "ss:///"

// remote splits ss:///bucket/path; ok is false for a local path.
func remote(s string) (bucket, path string, ok bool) {
	rest, ok := strings.CutPrefix(s, remotePrefix)
	if !ok {
		return "", "", false
	}
	bucket, path, _ = strings.Cut(rest, "/")
	return bucket, path, true
}

func (a *App) storageBuckets(args []string) error {
	pos, err := parse(flag.NewFlagSet("storage buckets list", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "storage buckets list <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetProjectFilesWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		o := r.JSON200
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "BUCKET\tACCESS\tFILES\tSIZE\tSIZE LIMIT\tTYPES")
		for _, b := range o.Buckets {
			access, limit, types := "private", "-", "any"
			if b.Public {
				access = "public"
			}
			if b.FileSizeLimit != nil {
				limit = humanBytes(*b.FileSizeLimit)
			}
			if len(b.AllowedMimeTypes) > 0 {
				types = strings.Join(b.AllowedMimeTypes, ",")
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", b.Id, access, b.Objects, humanBytes(b.Bytes), limit, types)
		}
		_ = tw.Flush()
		quota := "no quota"
		if o.QuotaBytes != nil {
			quota = "of " + humanBytes(*o.QuotaBytes)
		}
		fmt.Fprintf(w, "%d files, %s stored (%s)\n", o.Objects, humanBytes(o.Bytes), quota)
		if o.MissingObjects > 0 {
			fmt.Fprintf(w, "%d files have no data (for example %s)\n", o.MissingObjects, strings.Join(o.MissingSample, ", "))
		}
	})
}

func (a *App) storageBucketsCreate(args []string) error {
	fs := flag.NewFlagSet("storage buckets create", flag.ContinueOnError)
	public := fs.Bool("public", false, "anyone with a file's URL can download it")
	limit := fs.String("size-limit", "", `the largest file ("10MB"); the plan's limit by default`)
	types := fs.String("types", "", "allowed MIME types, comma-separated (image/*,application/pdf)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "storage buckets create <project> <bucket> [--public] [--size-limit 10MB] [--types image/*]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	in := client.StorageBucketInput{Id: pos[1], Public: public}
	if *limit != "" {
		n, err := parseSize(*limit)
		if err != nil {
			return usageErrorf("--size-limit: %v", err)
		}
		in.FileSizeLimit = &n
	}
	if *types != "" {
		ts := strings.Split(*types, ",")
		in.AllowedMimeTypes = &ts
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateStorageBucketWithResponse(c, p.Id, in)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON201, func(w io.Writer) { fmt.Fprintf(w, "Created bucket %s\n", r.JSON201.Id) })
}

func (a *App) storageBucketsDelete(args []string) error {
	fs := flag.NewFlagSet("storage buckets delete", flag.ContinueOnError)
	force := fs.Bool("force", false, "delete the bucket's files too")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "storage buckets delete <project> <bucket> [--force]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.DeleteStorageBucketWithResponse(c, p.Id, pos[1], &client.DeleteStorageBucketParams{Empty: force})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(map[string]string{"deleted": pos[1]}, func(w io.Writer) { fmt.Fprintf(w, "Deleted bucket %s\n", pos[1]) })
}

func (a *App) storageLs(args []string) error {
	pos, err := parse(flag.NewFlagSet("storage ls", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "storage ls <project> ss:///<bucket>[/folder/]"); err != nil {
		return err
	}
	bucket, prefix, ok := remote(pos[1])
	if !ok || bucket == "" {
		return usageErrorf("usage: pgdock storage ls <project> ss:///<bucket>[/folder/]")
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	var items []client.StorageEntry
	cursor := ""
	for {
		c, cancel := ctx()
		params := &client.ListStorageObjectsParams{Prefix: &prefix}
		if cursor != "" {
			params.Cursor = &cursor
		}
		r, err := a.api.ListStorageObjectsWithResponse(c, p.Id, bucket, params)
		cancel()
		if err := check(r, err); err != nil {
			return err
		}
		items = append(items, r.JSON200.Items...)
		if r.JSON200.NextCursor == nil {
			break
		}
		cursor = *r.JSON200.NextCursor
	}
	return a.emit(items, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, e := range items {
			if e.Folder {
				fmt.Fprintf(tw, "%s/\t\t\t\n", e.Name)
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, humanBytes(e.Object.Size), e.Object.MimeType, e.Object.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
		_ = tw.Flush()
	})
}

func (a *App) storageCp(args []string) error {
	usage := "storage cp <project> <file> ss:///<bucket>/<path> | ss:///<bucket>/<path> <file|->"
	pos, err := parse(flag.NewFlagSet("storage cp", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 3, usage); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	sb, sp, sr := remote(pos[1])
	db, dp, dr := remote(pos[2])
	switch {
	case !sr && dr:
		if dp == "" || strings.HasSuffix(dp, "/") {
			dp += filepath.Base(pos[1])
		}
		return a.upload(p, pos[1], db, dp)
	case sr && !dr:
		if sp == "" {
			return usageErrorf("usage: pgdock %s", usage)
		}
		return a.download(p, sb, sp, pos[2])
	}
	return usageErrorf("usage: pgdock %s (one side local, the other ss:///…)", usage)
}

func (a *App) upload(p client.Project, local, bucket, path string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	ct := mime.TypeByExtension(filepath.Ext(local))
	if ct == "" {
		ct = "application/octet-stream" // the server sniffs it
	}
	c, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	r, err := a.api.UploadStorageObjectWithBodyWithResponse(c, p.Id, bucket, &client.UploadStorageObjectParams{Path: path}, ct, f)
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) {
		fmt.Fprintf(w, "Uploaded %s to ss:///%s/%s (%s, %s)\n", local, bucket, path, humanBytes(r.JSON200.Size), r.JSON200.MimeType)
	})
}

func (a *App) download(p client.Project, bucket, path, local string) error {
	c, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	resp, err := a.api.DownloadStorageObject(c, p.Id, bucket, &client.DownloadStorageObjectParams{Path: path})
	if err != nil {
		return fmt.Errorf("could not reach the server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e client.Error
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			return &apiError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
		}
		return &apiError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	if local == "-" {
		_, err := io.Copy(a.Stdout, resp.Body)
		return err
	}
	if st, err := os.Stat(local); err == nil && st.IsDir() {
		local = filepath.Join(local, filepath.Base(path))
	}
	f, err := os.Create(local)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(local)
		return fmt.Errorf("download: %w", err)
	}
	return a.emit(map[string]any{"file": local, "bytes": n}, func(w io.Writer) {
		fmt.Fprintf(w, "Saved %s (%s)\n", local, humanBytes(n))
	})
}

func (a *App) storageRm(args []string) error {
	usage := "storage rm <project> ss:///<bucket>/<path>…"
	pos, err := parse(flag.NewFlagSet("storage rm", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usageErrorf("usage: pgdock %s", usage)
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	byBucket := map[string][]string{}
	var order []string
	for _, s := range pos[1:] {
		b, path, ok := remote(s)
		if !ok || path == "" {
			return usageErrorf("usage: pgdock %s", usage)
		}
		if _, seen := byBucket[b]; !seen {
			order = append(order, b)
		}
		byBucket[b] = append(byBucket[b], path)
	}
	var deleted int64
	for _, b := range order {
		c, cancel := ctx()
		r, err := a.api.DeleteStorageObjectsWithResponse(c, p.Id, b, client.DeleteStorageObjectsJSONRequestBody{Paths: byBucket[b]})
		cancel()
		if err := check(r, err); err != nil {
			return err
		}
		var out struct {
			Deleted int64 `json:"deleted"`
		}
		_ = json.Unmarshal(r.Body, &out)
		deleted += out.Deleted
	}
	return a.emit(map[string]int64{"deleted": deleted}, func(w io.Writer) { fmt.Fprintf(w, "Deleted %d file(s)\n", deleted) })
}

func (a *App) storageSign(args []string) error {
	fs := flag.NewFlagSet("storage sign", flag.ContinueOnError)
	expires := fs.Duration("expires", time.Hour, "how long the URL works (at most 168h)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "storage sign <project> ss:///<bucket>/<path> [--expires 1h]"); err != nil {
		return err
	}
	b, path, ok := remote(pos[1])
	if !ok || path == "" {
		return usageErrorf("usage: pgdock storage sign <project> ss:///<bucket>/<path> [--expires 1h]")
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	secs := int(expires.Seconds())
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.SignStorageObjectWithResponse(c, p.Id, b, client.SignStorageObjectJSONRequestBody{Path: path, ExpiresIn: &secs})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200, func(w io.Writer) { fmt.Fprintln(w, r.JSON200.SignedUrl) })
}

// parseSize reads "10MB", "1.5GiB" or plain bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		n      int64
	}{{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if t, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = strings.TrimSpace(t), u.n
			break
		}
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err != nil || f < 0 {
		return 0, fmt.Errorf("%q isn't a size", s)
	}
	return int64(f * float64(mult)), nil
}
