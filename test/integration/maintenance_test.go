package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestAnnouncedMaintenanceSLA is V3.1-M3's done-when (V3.1 §4): an
// announcement made 72 hours ahead excludes its window's minutes from the
// SLA of the HA projects it covers, and only those; one made with less
// notice excludes only what the notice covers; the minor-upgrade sweep
// waits for an announced window before restarting an HA project; covered
// organisations are emailed; announcements can be cancelled and end by
// themselves.
func TestAnnouncedMaintenanceSLA(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	q := store.New(e.DB)

	pa := e.CreateProject("maint-a").Project.Id
	pb := e.CreateProject("maint-b").Project.Id
	a, err := q.GetProject(ctx, pa)
	if err != nil {
		t.Fatal(err)
	}
	// The SLA covers HA projects: treat both projects' instance as one.
	for _, id := range []uuid.UUID{pa, pb} {
		if _, err := e.DB.Exec(ctx, `UPDATE instances SET ha_enabled = true WHERE id = (SELECT instance_id FROM projects WHERE id = $1)`, id); err != nil {
			t.Fatal(err)
		}
	}

	// An announcement through the API: a window in the future, scoped to
	// project A, emailed to its organisation's owner.
	start := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Minute)
	title := "Kernel updates"
	var ann gen.MaintenanceAnnouncement
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements", gen.MaintenanceAnnouncementRequest{
		Title: &title, Start: start, End: start.Add(time.Hour), ProjectIds: &[]uuid.UUID{pa},
	}, &ann); code != http.StatusCreated {
		t.Fatalf("announce: %d", code)
	}
	if ann.ShortNotice == nil || !*ann.ShortNotice || ann.Emailed == nil || *ann.Emailed < 1 ||
		len(ann.ScopeProjects) != 1 || ann.Incident.Severity != gen.IncidentSeverityMaintenance || ann.Incident.ResolvedAt != nil {
		t.Fatalf("announced: %+v", ann)
	}
	if !strings.HasPrefix(ann.Incident.Updates[0].Body, "Scheduled for ") {
		t.Fatalf("first update: %q", ann.Incident.Updates[0].Body)
	}
	waitFor(t, 30*time.Second, "the announcement email", func() bool {
		return e.SMTP.Count(testenv.OwnerEmail, "Kernel updates") > 0 || e.SMTP.Count(testenv.OwnerEmail, "Scheduled for") > 0
	})
	var apiErr gen.Error
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements", gen.MaintenanceAnnouncementRequest{
		Start: start, End: start.Add(25 * time.Hour)}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("a 25-hour window: %d %+v", code, apiErr)
	}
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements", gen.MaintenanceAnnouncementRequest{
		Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("a window in the past: %d %+v", code, apiErr)
	}

	// Move it into the past, as if announced 73 hours before its window:
	// an hour ending an hour ago. Every one of its minutes is an outage.
	now := time.Now().UTC().Truncate(time.Minute)
	w1s, w1e := now.Add(-2*time.Hour), now.Add(-time.Hour)
	if _, err := e.DB.Exec(ctx, `UPDATE incidents SET scheduled_start = $2, started_at = $2, scheduled_end = $3, announced_at = $4 WHERE id = $1`,
		ann.Incident.Id, w1s, w1e, w1s.Add(-73*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A region-wide one announced only 71 hours before a 30-minute window:
	// the notice covers none of it.
	w2s := now.Add(-3 * time.Hour)
	var short uuid.UUID
	if err := e.DB.QueryRow(ctx, `INSERT INTO incidents (title, components, region_id, severity, status, started_at, scheduled_start, scheduled_end, announced_at)
		VALUES ('Short notice', '{dedicated}', $1, 'maintenance', 'identified', $2, $2, $3, $4) RETURNING id`,
		a.Region, w2s, w2s.Add(30*time.Minute), w2s.Add(-71*time.Hour)).Scan(&short); err != nil {
		t.Fatal(err)
	}
	minutes := func(p uuid.UUID, from time.Time, n int, ok bool) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `INSERT INTO availability_minutes (project_id, minute, internal_ok, external_ok, excluded)
			SELECT $1, $2::timestamptz + make_interval(mins => g), $4, $4, false FROM generate_series(0, $3 - 1) g`, p, from, n, ok); err != nil {
			t.Fatal(err)
		}
	}
	minutes(pa, w1s, 60, false) // inside the announced window
	minutes(pb, w1s, 60, false) // same time, not covered
	minutes(pa, w2s, 30, true)  // inside the short-notice window
	minutes(pb, w2s, 30, false)

	n, err := e.Dedicated.ApplyMaintenanceExclusions(ctx, now.Add(-6*time.Hour), now.Add(time.Minute))
	if err != nil || n != 60 {
		t.Fatalf("exclusions: %d %v (want project A's 60 minutes)", n, err)
	}
	// Idempotent: a second pass excludes nothing more.
	if n, err := e.Dedicated.ApplyMaintenanceExclusions(ctx, now.Add(-6*time.Hour), now.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("second pass: %d %v", n, err)
	}

	ha := func(p uuid.UUID) gen.Availability {
		t.Helper()
		var st gen.HAStatus
		if code := e.Do("GET", "/api/v1/projects/"+p.String()+"/ha", nil, &st); code != http.StatusOK || st.Availability == nil {
			t.Fatalf("HA status: %d %+v", code, st)
		}
		return *st.Availability
	}
	av := ha(pa)
	if av.UnavailableMinutes != 0 || av.MeasuredMinutes != 30 || av.ExcludedMinutes == nil || *av.ExcludedMinutes != 60 ||
		av.Exclusions == nil || len(*av.Exclusions) != 1 || (*av.Exclusions)[0].IncidentId != ann.Incident.Id || (*av.Exclusions)[0].Title != title {
		t.Fatalf("project A availability: %+v", av)
	}
	if bv := ha(pb); bv.UnavailableMinutes != 90 || bv.ExcludedMinutes == nil || *bv.ExcludedMinutes != 0 {
		t.Fatalf("project B availability (nothing excluded): %+v", bv)
	}

	// Both windows have ended: the sweep resolves them.
	if err := e.Incidents.EndMaintenance(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{ann.Incident.Id, short} {
		inc, err := q.GetIncident(ctx, id)
		if err != nil || inc.ResolvedAt == nil || inc.Status != "resolved" {
			t.Fatalf("ended %s: %+v %v", id, inc, err)
		}
	}

	// The minor-upgrade sweep with the gate on: an HA instance behind on
	// its release waits for an announced window.
	e.Dedicated.SetRequireAnnouncement(true)
	t.Cleanup(func() { e.Dedicated.SetRequireAnnouncement(false) })
	if _, err := e.DB.Exec(ctx, `UPDATE instances SET pg_release = '17.1', pg_release_available = '17.2', release_checked_at = now()
		WHERE id = $1`, a.InstanceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.DB.Exec(context.Background(), `UPDATE instances SET pg_release = NULL, pg_release_available = NULL WHERE id = $1`, a.InstanceID)
	})
	sweepAt := time.Now().UTC()
	here := gen.MaintenanceWindow{Enabled: true, Weekday: int(sweepAt.Weekday()), StartHour: sweepAt.Hour(), Hours: 2}
	if code := e.Do("PUT", "/api/v1/admin/maintenance/window", here, nil); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("window: %d", code)
	}
	if inst, err := e.Dedicated.MaintenanceSweep(ctx, sweepAt); err != nil || inst != nil {
		t.Fatalf("unannounced HA sweep: %+v %v (want it skipped)", inst, err)
	}
	// An announcement covering now, made 73 hours ago, opens the gate.
	var open uuid.UUID
	if err := e.DB.QueryRow(ctx, `INSERT INTO incidents (title, components, severity, status, started_at, scheduled_start, scheduled_end, announced_at)
		VALUES ('Upgrades', '{dedicated}', 'maintenance', 'identified', $1, $1, $2, $3) RETURNING id`,
		sweepAt.Add(-time.Minute), sweepAt.Add(time.Hour), sweepAt.Add(-73*time.Hour)).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AnnouncedMaintenanceFor(ctx, store.AnnouncedMaintenanceForParams{At: sweepAt, ProjectID: pa}); err != nil {
		t.Fatalf("announced window for the sweep: %v", err)
	}
	// Announced only now, it doesn't.
	if _, err := e.DB.Exec(ctx, `UPDATE incidents SET announced_at = now() WHERE id = $1`, open); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AnnouncedMaintenanceFor(ctx, store.AnnouncedMaintenanceForParams{At: sweepAt, ProjectID: pa}); err == nil {
		t.Fatal("a window announced without notice opened the sweep's gate")
	}

	// Cancelling: an open announcement is called off and listed as such;
	// an ended one can't be.
	var later gen.MaintenanceAnnouncement
	if code := e.Do("POST", "/api/v1/admin/maintenance/announcements", gen.MaintenanceAnnouncementRequest{
		Start: start.Add(96 * time.Hour), End: start.Add(97 * time.Hour)}, &later); code != http.StatusCreated || later.ShortNotice == nil || *later.ShortNotice {
		t.Fatalf("announce later: %d %+v", code, later)
	}
	var cancelled gen.MaintenanceAnnouncement
	if code := e.Do("DELETE", "/api/v1/admin/maintenance/announcements/"+later.Incident.Id.String(), nil, &cancelled); code != http.StatusOK ||
		cancelled.CancelledAt == nil || cancelled.Incident.ResolvedAt == nil {
		t.Fatalf("cancel: %d %+v", code, cancelled)
	}
	if code := e.Do("DELETE", "/api/v1/admin/maintenance/announcements/"+ann.Incident.Id.String(), nil, &apiErr); code != http.StatusNotFound {
		t.Fatalf("cancel an ended window: %d", code)
	}
	var list gen.MaintenanceAnnouncementList
	if code := e.Do("GET", "/api/v1/admin/maintenance/announcements", nil, &list); code != http.StatusOK || len(list.Items) < 3 {
		t.Fatalf("list: %d %+v", code, list)
	}
}
