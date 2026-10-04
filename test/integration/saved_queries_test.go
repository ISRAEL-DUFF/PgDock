package integration

import (
	"net/http"
	"testing"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestSavedQueries is phase 3's API (docs/ui-redesign.md): private and
// shared queries, owners editing their own, admins deleting shared ones,
// favourites per person, and queries going with the project.
func TestSavedQueries(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("Queries")
	id := c.Project.Id.String()
	base := "/api/v1/projects/" + id + "/queries"
	var inv gen.InvitationCreated
	if code := e.Do("POST", "/api/v1/orgs/"+e.OrgID.String()+"/members", map[string]any{"email": "viewer@example.com", "role": "member",
		"projects": []map[string]any{{"project_id": c.Project.Id, "role": "read_only"}}}, &inv); code != http.StatusCreated {
		t.Fatalf("invite: %d", code)
	}
	viewer := e.AcceptInvitation(e.MailToken("viewer@example.com", "invitation"), "viewer@example.com")

	// The owner saves a private and a shared query.
	var mine, team gen.SavedQuery
	if code := e.Do("POST", base, map[string]any{"name": "Mine", "sql": "select 1"}, &mine); code != http.StatusCreated || mine.Visibility != "private" || !mine.Mine {
		t.Fatalf("create: %d %+v", code, mine)
	}
	if code := e.Do("POST", base, map[string]any{"name": "Team report", "sql": "select 2", "visibility": "shared"}, &team); code != http.StatusCreated {
		t.Fatalf("create shared: %d", code)
	}
	if code := e.Do("POST", base, map[string]any{"name": " "}, nil); code != http.StatusBadRequest {
		t.Fatalf("blank name: %d", code)
	}

	// A read-only member sees only the shared one, can't change it, and
	// keeps their own queries and favourites.
	var list gen.SavedQueryList
	if code := viewer.Do("GET", base, nil, &list); code != http.StatusOK || len(list.Items) != 1 || list.Items[0].Id != team.Id || list.Items[0].Mine {
		t.Fatalf("viewer list: %d %+v", code, list.Items)
	}
	if code := viewer.Do("GET", base+"/"+mine.Id.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("viewer reads a private query: %d", code)
	}
	if code := viewer.Do("PATCH", base+"/"+team.Id.String(), map[string]any{"sql": "drop table x"}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer edits a shared query: %d", code)
	}
	if code := viewer.Do("DELETE", base+"/"+team.Id.String(), nil, nil); code != http.StatusForbidden {
		t.Fatalf("viewer deletes a shared query: %d", code)
	}
	var fav gen.SavedQuery
	if code := viewer.Do("PUT", base+"/"+team.Id.String()+"/favorite", map[string]any{"favorite": true}, &fav); code != http.StatusOK || !fav.Favorite {
		t.Fatalf("favourite: %d %+v", code, fav)
	}
	var own gen.SavedQuery
	if code := viewer.Do("POST", base, map[string]any{"name": "Viewer's", "sql": "select 3"}, &own); code != http.StatusCreated {
		t.Fatalf("viewer saves: %d", code)
	}
	list = gen.SavedQueryList{}
	e.Do("GET", base, nil, &list)
	if len(list.Items) != 2 {
		t.Fatalf("owner sees another member's private query: %+v", list.Items)
	}
	for _, q := range list.Items {
		if q.Favorite {
			t.Fatalf("favourites are per person: %+v", q)
		}
	}

	// Owners edit theirs; sharing makes it visible.
	var upd gen.SavedQuery
	if code := e.Do("PATCH", base+"/"+mine.Id.String(), map[string]any{"name": "Mine, renamed", "sql": "select 10", "visibility": "shared"}, &upd); code != http.StatusOK ||
		upd.Name != "Mine, renamed" || upd.Sql != "select 10" || !upd.UpdatedAt.After(mine.UpdatedAt) {
		t.Fatalf("update: %d %+v", code, upd)
	}
	list = gen.SavedQueryList{}
	viewer.Do("GET", base, nil, &list)
	if len(list.Items) != 3 || list.Items[0].Id != own.Id && list.Items[0].Id != mine.Id {
		t.Fatalf("viewer after sharing: %+v", list.Items)
	}

	// A project admin may delete a shared query; the project's deletion
	// takes the rest.
	if code := e.Do("DELETE", base+"/"+team.Id.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("owner deletes shared: %d", code)
	}
	if code := e.Do("GET", base+"/"+team.Id.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("deleted query: %d", code)
	}
	var left int
	if err := e.DB.QueryRow(t.Context(), `SELECT count(*) FROM saved_queries WHERE project_id = $1`, c.Project.Id).Scan(&left); err != nil || left != 2 {
		t.Fatalf("queries left: %d %v", left, err)
	}
}
