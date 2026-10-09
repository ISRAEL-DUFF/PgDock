package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/incidents"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// mailParts splits a received message (its lines joined with newlines)
// into its Subject header and its body.
func mailParts(m testenv.Mail) (subject, body string) {
	head, body, _ := strings.Cut(m.Data, "\n\n")
	for _, l := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(l, "Subject: "); ok {
			subject = v
		}
	}
	return subject, strings.TrimRight(body, "\n")
}

// TestMaintenanceProposals is V4.1-M7's done-when (V4.1 §8.2, §8.3): an
// HA instance behind on its minor release produces a draft for the first
// window at least 96 hours away; a draft isn't announced, emailed or on
// the status page; discarding it leaves the work waiting (and it isn't
// proposed again for that window); confirming one emails exactly what its
// preview showed and, 73 hours on, opens the gate for the upgrade.
func TestMaintenanceProposals(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	q := store.New(e.DB)
	pid := e.CreateProject("proposals").Project.Id
	p, err := q.GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE instances SET ha_enabled = true, pg_release = '17.1', pg_release_available = '17.2', release_checked_at = now() + interval '30 days'
		WHERE id = $1`, p.InstanceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.DB.Exec(context.Background(), `UPDATE instances SET ha_enabled = false, pg_release = NULL, pg_release_available = NULL WHERE id = $1`, p.InstanceID)
	})
	e.Dedicated.SetRequireAnnouncement(true)
	t.Cleanup(func() { e.Dedicated.SetRequireAnnouncement(false) })
	// A weekly window three days from today, so neither now nor a week on
	// is inside it.
	now := time.Now().UTC()
	window := gen.MaintenanceWindow{Enabled: true, Weekday: (int(now.Weekday()) + 3) % 7, StartHour: 2, Hours: 4}
	if code := e.Do("PUT", "/api/v1/admin/maintenance/window", window, nil); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("window: %d", code)
	}

	drafts := func() []gen.MaintenanceAnnouncement {
		t.Helper()
		var list gen.MaintenanceAnnouncementList
		if code := e.Do("GET", "/api/v1/admin/maintenance/announcements", nil, &list); code != http.StatusOK {
			t.Fatalf("list: %d", code)
		}
		var out []gen.MaintenanceAnnouncement
		for _, a := range list.Items {
			if a.Draft != nil && *a.Draft {
				out = append(out, a)
			}
		}
		return out
	}
	sweep := func(at time.Time) {
		t.Helper()
		if inst, err := e.Dedicated.MaintenanceSweep(ctx, at); err != nil || inst != nil {
			t.Fatalf("sweep at %s: %+v %v", at, inst, err)
		}
	}
	gateOpen := func(at time.Time) bool {
		_, err := q.AnnouncedMaintenanceFor(ctx, store.AnnouncedMaintenanceForParams{At: at, ProjectID: pid})
		return err == nil
	}

	// The sweep proposes a draft, once, for the first window ≥ 96 h away.
	sweep(now)
	sweep(now)
	ds := drafts()
	if len(ds) != 1 {
		t.Fatalf("drafts after two sweeps: %+v", ds)
	}
	d1 := ds[0]
	if d1.ScheduledStart == nil || d1.ScheduledStart.Before(now.Add(96*time.Hour)) || d1.ScheduledStart.After(now.Add(96*time.Hour+7*24*time.Hour)) ||
		d1.ScheduledStart.UTC().Weekday() != time.Weekday(window.Weekday) || d1.ScheduledStart.UTC().Hour() != 2 ||
		d1.AnnouncedAt != nil || d1.ProposedFor == nil || *d1.ProposedFor != "minor_upgrade" ||
		len(d1.ScopeProjects) != 1 || d1.ScopeProjects[0] != pid || d1.Incident.Status != "draft" {
		t.Fatalf("the draft: %+v", d1)
	}
	// Not announced: no email, not for the status page, no exclusions, no
	// banner, and the gate stays shut in its window.
	if n := e.SMTP.Count(testenv.OwnerEmail, "Postgres minor upgrade"); n != 0 {
		t.Fatalf("a draft emailed %d times", n)
	}
	unpushed, err := q.UnpushedIncidents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range unpushed {
		if u.ID == d1.Incident.Id {
			t.Fatal("a draft is due for the status page")
		}
	}
	inWindow := d1.ScheduledStart.Add(time.Minute)
	if gateOpen(inWindow) {
		t.Fatal("a draft opened the gate")
	}

	// Discarding it: the work keeps waiting, and the window isn't proposed again.
	var disc gen.MaintenanceAnnouncement
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements/"+d1.Incident.Id.String()+"/discard", nil, &disc); code != http.StatusOK ||
		disc.CancelledAt == nil || disc.Incident.Status != "resolved" {
		t.Fatalf("discard: %d %+v", code, disc)
	}
	sweep(now)
	if ds := drafts(); len(ds) != 0 {
		t.Fatalf("re-proposed the discarded window: %+v", ds)
	}
	if gateOpen(inWindow) {
		t.Fatal("a discarded draft opened the gate")
	}

	// A week on, the next window is proposed.
	sweep(now.Add(7 * 24 * time.Hour))
	ds = drafts()
	if len(ds) != 1 || !ds[0].ScheduledStart.After(*d1.ScheduledStart) {
		t.Fatalf("next week's draft: %+v", ds)
	}
	d2 := ds[0]

	// The preview is the email, byte for byte.
	var pv gen.MaintenancePreview
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements/preview", gen.MaintenancePreviewRequest{IncidentId: &d2.Incident.Id}, &pv); code != http.StatusOK ||
		pv.Organisations != 1 || pv.Addresses < 1 || !strings.Contains(pv.Body, "Postgres minor upgrade") && !strings.Contains(pv.Subject, "Postgres minor upgrade") {
		t.Fatalf("preview: %d %+v", code, pv)
	}
	var conf gen.MaintenanceAnnouncement
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements/"+d2.Incident.Id.String()+"/confirm", nil, &conf); code != http.StatusOK ||
		conf.AnnouncedAt == nil || conf.Emailed == nil || *conf.Emailed != pv.Addresses || conf.Incident.Status != "identified" ||
		conf.ShortNotice == nil || *conf.ShortNotice {
		t.Fatalf("confirm: %d %+v", code, conf)
	}
	waitFor(t, 20*time.Second, "the announcement email", func() bool { return e.SMTP.Count(testenv.OwnerEmail, "Scheduled for") > 0 })
	var sent testenv.Mail
	for _, m := range e.SMTP.Mail() {
		if strings.Contains(m.Data, "X-PGDock-Event: maintenance") {
			sent = m
		}
	}
	subject, body := mailParts(sent)
	if subject != pv.Subject || body != strings.TrimRight(pv.Body, "\n") {
		t.Fatalf("the email isn't its preview:\nsubject %q\n   want %q\nbody %q\n want %q", subject, pv.Subject, body, pv.Body)
	}
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements/"+d2.Incident.Id.String()+"/confirm", nil, nil); code != http.StatusNotFound {
		t.Fatalf("confirm twice: %d", code)
	}

	// 73 hours on (inside its window, at least 96 h after the confirmation)
	// the gate lets the upgrade run; before the notice has run, it doesn't.
	at := d2.ScheduledStart.Add(time.Minute)
	if at.Sub(*conf.AnnouncedAt) < 73*time.Hour || !gateOpen(at) {
		t.Fatalf("the gate at %s, announced %s: shut", at, conf.AnnouncedAt)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE incidents SET announced_at = $2 WHERE id = $1`, d2.Incident.Id, at.Add(-71*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if gateOpen(at) {
		t.Fatal("71 hours' notice opened the gate")
	}

	// The schedule form's preview is the email announcing would send.
	start := time.Now().UTC().Add(100 * time.Hour).Truncate(time.Minute)
	title, msg := "Kernel updates", "Nodes restart onto a new kernel."
	req := gen.MaintenancePreviewRequest{Title: &title, Body: &msg, Start: &start, End: ptr(start.Add(time.Hour)), ProjectIds: &[]uuid.UUID{pid}}
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements/preview", req, &pv); code != http.StatusOK || pv.Organisations != 1 {
		t.Fatalf("form preview: %d %+v", code, pv)
	}
	before := len(e.SMTP.Mail())
	var ann gen.MaintenanceAnnouncement
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements", gen.MaintenanceAnnouncementRequest{Title: &title, Body: &msg, Start: start, End: start.Add(time.Hour),
		ProjectIds: &[]uuid.UUID{pid}}, &ann); code != http.StatusCreated {
		t.Fatalf("announce: %d", code)
	}
	waitFor(t, 20*time.Second, "the second email", func() bool { return len(e.SMTP.Mail()) > before })
	subject, body = mailParts(e.SMTP.Mail()[len(e.SMTP.Mail())-1])
	if subject != pv.Subject || body != strings.TrimRight(pv.Body, "\n") {
		t.Fatalf("the announcement isn't its preview:\n%q\n%q", body, pv.Body)
	}

	// A draft whose window starts unconfirmed lapses.
	other := e.CreateProject("proposals-2").Project.Id
	lapsing, err := e.Incidents.Propose(ctx, incidents.Proposal{Reason: "test", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour),
		Projects: []uuid.UUID{pid, other}}, now)
	if err != nil || lapsing == nil {
		t.Fatalf("propose: %+v %v", lapsing, err)
	}
	if sc, _ := q.IncidentScope(ctx, lapsing.ID); len(sc) != 1 || *sc[0].ProjectID != other {
		t.Fatalf("a project with maintenance announced was proposed again: %+v", sc)
	}
	if err := e.Incidents.EndMaintenance(ctx, now.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if inc, err := q.GetIncident(ctx, lapsing.ID); err != nil || inc.Status != "resolved" || inc.CancelledAt == nil {
		t.Fatalf("a draft outlived its window's start: %+v %v", inc, err)
	}
}
