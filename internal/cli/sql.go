package cli

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/client"
)

func (a *App) sql(args []string) error {
	fs := flag.NewFlagSet("sql", flag.ContinueOnError)
	command := fs.String("c", "", "the SQL to run")
	file := fs.String("f", "", "a file of SQL to run (- for stdin)")
	asCSV := fs.Bool("csv", false, "print the last result as CSV")
	readOnly := fs.Bool("read-only", false, "run read-only")
	timeout := fs.Int("timeout", 0, "statement timeout in seconds (default 30)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, `sql <project> -c "select …" | -f file.sql [--json|--csv]`); err != nil {
		return err
	}
	q := *command
	switch {
	case q != "" && *file != "":
		return usageErrorf("give -c or -f, not both")
	case *file == "-":
		b, err := io.ReadAll(a.Stdin)
		if err != nil {
			return err
		}
		q = string(b)
	case *file != "":
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		q = string(b)
	}
	if strings.TrimSpace(q) == "" {
		return usageErrorf(`usage: pgdock sql <project> -c "select …" | -f file.sql`)
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.SqlRequest{Query: q, QueryId: uuid.New()}
	if *readOnly {
		req.ReadOnly = readOnly
	}
	if *timeout > 0 {
		req.TimeoutSeconds = timeout
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.RunSQLWithResponse(c, p.Id, req)
	if err := check(r, err); err != nil {
		return err
	}
	res := r.JSON200
	switch {
	case a.json:
		if err := a.emit(res, nil); err != nil {
			return err
		}
	case *asCSV:
		if err := writeCSV(a.Stdout, res); err != nil {
			return err
		}
	default:
		printResults(a.Stdout, res)
	}
	for _, n := range res.Notices {
		fmt.Fprintln(a.Stderr, "NOTICE: "+n)
	}
	if res.Error != nil {
		msg := res.Error.Message
		if res.Error.Code != nil && *res.Error.Code != "" {
			msg += " (SQLSTATE " + *res.Error.Code + ")"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// writeCSV writes the last result as CSV.
func writeCSV(out io.Writer, res *client.SqlResult) error {
	if len(res.Results) == 0 {
		return nil
	}
	last := res.Results[len(res.Results)-1]
	w := csv.NewWriter(out)
	head := make([]string, len(last.Columns))
	for i, col := range last.Columns {
		head[i] = col.Name
	}
	_ = w.Write(head)
	for _, row := range last.Rows {
		_ = w.Write(cells(row, ""))
	}
	w.Flush()
	return w.Error()
}

func cells(row []*string, null string) []string {
	out := make([]string, len(row))
	for i, v := range row {
		if v == nil {
			out[i] = null
		} else {
			out[i] = *v
		}
	}
	return out
}

func printResults(out io.Writer, res *client.SqlResult) {
	for i, r := range res.Results {
		if i > 0 {
			fmt.Fprintln(out)
		}
		if len(r.Columns) > 0 {
			w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			head := make([]string, len(r.Columns))
			for j, col := range r.Columns {
				head[j] = col.Name
			}
			fmt.Fprintln(w, strings.Join(head, "\t"))
			for _, row := range r.Rows {
				fmt.Fprintln(w, strings.Join(cells(row, "NULL"), "\t"))
			}
			_ = w.Flush()
			if r.Truncated {
				fmt.Fprintln(out, "(more rows than shown)")
			}
		}
		fmt.Fprintln(out, r.Command)
	}
}

// ---- Operations -----------------------------------------------------------------

func (a *App) operationsGet(args []string) error {
	fs := flag.NewFlagSet("operations get", flag.ContinueOnError)
	followFlag := fs.Bool("follow", false, "stream its log until it finishes")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "operations get <id> [--follow]"); err != nil {
		return err
	}
	id, err := uuid.Parse(pos[0])
	if err != nil {
		return usageErrorf("not an operation id: %s", pos[0])
	}
	if err := a.connectAPI(); err != nil {
		return err
	}
	if *followFlag {
		if err := a.follow(id, !a.json); err != nil {
			return err
		}
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.GetOperationWithResponse(c, id)
	if err := check(r, err); err != nil {
		return err
	}
	op := r.JSON200
	if err := a.emit(op, func(w io.Writer) {
		fmt.Fprintf(w, "Operation:\t%s\n", op.Id)
		fmt.Fprintf(w, "Kind:\t%s\n", op.Kind)
		fmt.Fprintf(w, "Status:\t%s\n", op.Status)
		fmt.Fprintf(w, "Attempts:\t%d\n", op.Attempts)
		fmt.Fprintf(w, "Created:\t%s\n", when(&op.CreatedAt))
		fmt.Fprintf(w, "Finished:\t%s\n", when(op.FinishedAt))
		if op.Error != nil {
			fmt.Fprintf(w, "Error:\t%s\n", *op.Error)
		}
		if !*followFlag {
			for _, l := range op.Log {
				fmt.Fprintf(w, "%s\t%s\t%s\n", l.Ts.Local().Format("15:04:05"), l.Step, l.Msg)
			}
		}
	}); err != nil {
		return err
	}
	if op.Status == client.OperationStatusFailed {
		return opFailed{"the operation failed"}
	}
	return nil
}

// follow streams an operation's log (to stderr when show) until it
// finishes; a failed operation is opFailed.
func (a *App) follow(id uuid.UUID, show bool) error {
	c, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(c, "GET", a.t.Server+"/api/v1/operations/"+id.String()+"/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.t.Token)
	req.Header.Set("Accept", "text/event-stream")
	res, err := a.hc.Do(req)
	if err != nil {
		return fmt.Errorf("follow operation %s: %w", id, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &apiError{Status: res.StatusCode, Message: "could not follow operation " + id.String()}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := []byte(strings.TrimPrefix(line, "data: "))
			switch event {
			case "log":
				var l client.OperationLogEntry
				if json.Unmarshal(data, &l) == nil && show {
					fmt.Fprintf(a.Stderr, "  %s  %-10s %s\n", l.Ts.Local().Format("15:04:05"), l.Step, l.Msg)
				}
			case "done":
				var d struct {
					Status string  `json:"status"`
					Error  *string `json:"error"`
				}
				_ = json.Unmarshal(data, &d)
				if d.Status != string(client.OperationStatusSucceeded) {
					msg := "operation " + id.String() + " " + d.Status
					if d.Error != nil {
						msg += ": " + *d.Error
					}
					return opFailed{msg}
				}
				return nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("follow operation %s: %w", id, err)
	}
	return fmt.Errorf("the stream for operation %s ended early; check it with pgdock operations get %s", id, id)
}

// ---- Backups --------------------------------------------------------------------

func (a *App) backupList(args []string) error {
	pos, err := parse(flag.NewFlagSet("backup list", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "backup list <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.ListBackupsWithResponse(c, &client.ListBackupsParams{ProjectId: &p.Id})
	if err := check(r, err); err != nil {
		return err
	}
	return a.emit(r.JSON200.Items, func(w io.Writer) {
		fmt.Fprintln(w, "ID\tKIND\tSTATUS\tSIZE\tSTARTED\tEXPIRES")
		for _, b := range r.JSON200.Items {
			size := "-"
			if b.SizeBytes != nil {
				size = humanBytes(*b.SizeBytes)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", b.Id, b.Kind, b.Status, size, when(&b.StartedAt), when(b.ExpiresAt))
		}
	})
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (a *App) backupCreate(args []string) error {
	pos, err := parse(flag.NewFlagSet("backup create", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "backup create <project>"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.CreateProjectBackupWithResponse(c, p.Id)
	if err := check(r, err); err != nil {
		return err
	}
	op := r.JSON202
	if !a.noWait {
		if err := a.follow(op.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(op, func(w io.Writer) { fmt.Fprintf(w, "Backed up %s (operation %s).\n", p.Name, op.Id) })
}

func (a *App) backupRestore(args []string) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	backup := fs.String("backup", "", "the backup's id")
	into := fs.String("into", "", "restore into a new project with this name")
	inPlace := fs.Bool("in-place", false, "replace the project's data (needs --confirm)")
	confirm := fs.String("confirm", "", "the project's name, typed, for --in-place")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "backup restore <project> --backup <id> [--into <name>] | --in-place --confirm <name>"); err != nil {
		return err
	}
	bid, err := uuid.Parse(*backup)
	if err != nil {
		return usageErrorf("--backup takes a backup id (pgdock backup list %s)", pos[0])
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	req := client.RestoreRequest{}
	if *inPlace {
		if *confirm != p.Name {
			return usageErrorf("restoring in place replaces %s's data: add --confirm %q", p.Name, p.Name)
		}
		m := client.InPlace
		req.Mode, req.Confirm = &m, confirm
	} else {
		m := client.New
		req.Mode = &m
		name := *into
		if name == "" {
			name = p.Name + " restored"
		}
		req.Name = &name
	}
	c, cancel := ctx()
	defer cancel()
	r, err := a.api.RestoreBackupWithResponse(c, bid, req)
	if err := check(r, err); err != nil {
		return err
	}
	res := r.JSON202
	if !a.noWait {
		if err := a.follow(res.Operation.Id, !a.json); err != nil {
			return err
		}
	}
	return a.emit(res, func(w io.Writer) {
		if res.Credentials != nil {
			fmt.Fprintf(w, "Restored into %s (%s).\n", res.Credentials.Project.Name, res.Credentials.Project.Id)
			fmt.Fprintf(w, "Pooled URL:\t%s\n", res.Credentials.Connection.PooledUrl)
			fmt.Fprintf(w, "Password:\t%s\n", res.Credentials.Password)
			return
		}
		fmt.Fprintf(w, "Restored %s in place.\n", p.Name)
	})
}

// backupDownload saves a backup as a plain pg_dump archive (organisation
// owners, V2 §10.10): the one named, or a fresh one.
func (a *App) backupDownload(args []string) error {
	fs := flag.NewFlagSet("backup download", flag.ContinueOnError)
	id := fs.String("backup", "", "the backup's id (default: back up now and download that)")
	out := fs.String("o", "", "the file to write (default <project>-<time>.dump)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "backup download <project> [--backup <id>] [-o file]"); err != nil {
		return err
	}
	p, err := a.project(pos[0])
	if err != nil {
		return err
	}
	if *id == "" {
		c, cancel := ctx()
		r, err := a.api.CreateProjectBackupWithResponse(c, p.Id)
		cancel()
		if err := check(r, err); err != nil {
			return err
		}
		if err := a.follow(r.JSON202.Id, !a.json); err != nil {
			return err
		}
		c, cancel = ctx()
		l, err := a.api.ListBackupsWithResponse(c, &client.ListBackupsParams{ProjectId: &p.Id})
		cancel()
		if err := check(l, err); err != nil {
			return err
		}
		for _, b := range l.JSON200.Items {
			if b.OperationId != nil && *b.OperationId == r.JSON202.Id {
				*id = b.Id.String()
			}
		}
		if *id == "" {
			return errors.New("the new backup is not listed")
		}
	}
	bid, err := uuid.Parse(*id)
	if err != nil {
		return fmt.Errorf("--backup: %w", err)
	}
	name := *out
	if name == "" {
		name = fmt.Sprintf("%s-%s.dump", p.Name, time.Now().UTC().Format("20060102-1504"))
		name = strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == ' ' {
				return '-'
			}
			return r
		}, name)
	}
	c, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	resp, err := a.api.DownloadBackup(c, bid)
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
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("download: %w", err)
	}
	return a.emit(map[string]any{"backup": bid, "file": name, "bytes": n}, func(w io.Writer) {
		fmt.Fprintf(w, "Saved %s (%s): restore it with pg_restore -d <url> %s\n", name, humanBytes(n), name)
	})
}
