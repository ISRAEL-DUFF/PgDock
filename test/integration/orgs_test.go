package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

func projectNames(t *testing.T, list gen.ProjectList) []string {
	t.Helper()
	var out []string
	for _, p := range list.Items {
		out = append(out, p.Name)
	}
	return out
}

// TestTwoOrgsSeeOnlyTheirOwn is the first part of M8's done-when: two
// users in two organisations each see only their own projects, and the
// other's ids answer 404 everywhere.
func TestTwoOrgsSeeOnlyTheirOwn(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	mine := e.CreateProject("Owner app")
	bob := e.InviteUser("bob@example.com")
	theirs := bob.CreateProject("Bob app", bob.OrgID)

	var list gen.ProjectList
	if code := e.Do("GET", "/api/v1/projects", nil, &list); code != http.StatusOK || strings.Join(projectNames(t, list), ",") != "Owner app" {
		t.Fatalf("owner's projects: %d %v", code, projectNames(t, list))
	}
	list = gen.ProjectList{}
	if code := bob.Do("GET", "/api/v1/projects", nil, &list); code != http.StatusOK || strings.Join(projectNames(t, list), ",") != "Bob app" {
		t.Fatalf("bob's projects: %d %v", code, projectNames(t, list))
	}
	if list.Items[0].MyRole == nil || *list.Items[0].MyRole != gen.ProjectRoleAdmin {
		t.Fatalf("bob owns his org, so he is admin of its projects: %+v", list.Items[0].MyRole)
	}

	// Every kind of reference to the other org's things is 404.
	var ownerOp gen.OperationList
	e.Do("GET", "/api/v1/operations?project_id="+mine.Project.Id.String(), nil, &ownerOp)
	if len(ownerOp.Items) == 0 {
		t.Fatal("no operations for the owner's project")
	}
	for _, p := range []string{
		"/api/v1/projects/" + mine.Project.Id.String(),
		"/api/v1/projects/" + mine.Project.Id.String() + "/metrics",
		"/api/v1/projects/" + mine.Project.Id.String() + "/members",
		"/api/v1/projects/" + mine.Project.Id.String() + "/schema",
		"/api/v1/operations/" + ownerOp.Items[0].Id.String(),
		"/api/v1/orgs/" + e.OrgID.String(),
		"/api/v1/orgs/" + e.OrgID.String() + "/members",
		"/api/v1/projects?org=" + e.OrgID.String(),
		"/api/v1/operations?org=" + e.OrgID.String(),
		"/api/v1/projects/" + uuid.New().String(), // and a project that doesn't exist looks the same
	} {
		if code := bob.Do("GET", p, nil, nil); code != http.StatusNotFound {
			t.Errorf("bob GET %s: %d, want 404", p, code)
		}
	}
	for _, p := range []string{
		"/api/v1/projects/" + theirs.Project.Id.String(),
		"/api/v1/orgs/" + bob.OrgID.String(),
	} {
		// The platform admin is not a member of bob's org (V2 §2.4).
		if code := e.Do("GET", p, nil, nil); code != http.StatusNotFound {
			t.Errorf("platform admin GET %s: %d, want 404", p, code)
		}
	}
	var wrote gen.SqlResult
	if code := bob.Do("POST", "/api/v1/projects/"+mine.Project.Id.String()+"/sql",
		gen.SqlRequest{Query: "SELECT 1", QueryId: uuid.New()}, &wrote); code != http.StatusNotFound {
		t.Errorf("bob runs SQL on the owner's project: %d", code)
	}
	if code := bob.Do("POST", "/api/v1/projects", map[string]any{"name": "Sneaky", "org_id": e.OrgID}, nil); code != http.StatusNotFound {
		t.Errorf("bob creates a project in the owner's org: %d", code)
	}
	// Platform routes stay the platform admin's.
	if code := bob.Do("GET", "/api/v1/nodes", nil, nil); code != http.StatusForbidden {
		t.Errorf("bob lists nodes: %d", code)
	}
}

// TestReadOnlyMemberCredentials is the rest of M8's done-when: an invited
// member with read-only project access gets working read-only
// credentials, and removing them revokes everything at once.
func TestReadOnlyMemberCredentials(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	app := e.CreateProject("Shop")
	pid := app.Project.Id.String()
	appConn := e.MustConnect(app.Connection.SessionUrl)
	if _, err := appConn.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, total numeric); INSERT INTO orders VALUES (1, 9.5)`); err != nil {
		t.Fatal(err)
	}

	// Invite carol to the org as a member with read-only access to Shop.
	var inv gen.InvitationCreated
	if code := e.Do("POST", "/api/v1/orgs/"+e.OrgID.String()+"/members", map[string]any{
		"email": "carol@example.com", "role": "member",
		"projects": []map[string]any{{"project_id": pid, "role": "read_only"}},
	}, &inv); code != http.StatusCreated || !inv.EmailSent || inv.Url == "" {
		t.Fatalf("invite: %d %+v", code, inv)
	}
	carol := e.AcceptInvitation(e.MailToken("carol@example.com", "invitation"), "carol@example.com")

	var list gen.ProjectList
	if code := carol.Do("GET", "/api/v1/projects?org="+e.OrgID.String(), nil, &list); code != http.StatusOK || len(list.Items) != 1 ||
		list.Items[0].MyRole == nil || *list.Items[0].MyRole != gen.ProjectRoleReadOnly {
		t.Fatalf("carol's view of the org: %d %+v", code, list)
	}
	var info gen.PersonalCredentialsInfo
	if code := carol.Do("GET", "/api/v1/projects/"+pid+"/credentials", nil, &info); code != http.StatusOK || info.Exists || info.Access != gen.PersonalCredentialsInfoAccessReadOnly {
		t.Fatalf("credentials before: %d %+v", code, info)
	}
	var creds gen.PersonalCredentials
	if code := carol.Do("POST", "/api/v1/projects/"+pid+"/credentials", nil, &creds); code != http.StatusOK || creds.Access != gen.PersonalCredentialsAccessReadOnly {
		t.Fatalf("issue credentials: %d %+v", code, creds)
	}
	if !strings.HasPrefix(creds.Role, app.Project.DbName+"_u_") || creds.Connection.User != creds.Role || !strings.Contains(creds.Connection.PooledUrl, creds.Password) {
		t.Fatalf("credentials: %+v", creds)
	}
	// Both poolers take the login; reads work and writes do not.
	for _, url := range []string{creds.Connection.SessionUrl, creds.Connection.PooledUrl} {
		c := e.MustConnect(url)
		var total string
		if err := c.QueryRow(ctx, `SELECT total::text FROM orders WHERE id = 1`).Scan(&total); err != nil || total != "9.5" {
			t.Fatalf("read through %s: %q %v", url, total, err)
		}
		for _, stmt := range []string{`INSERT INTO orders VALUES (2, 1)`, `UPDATE orders SET total = 0`, `CREATE TABLE sneaky (x int)`} {
			if _, err := c.Exec(ctx, stmt); err == nil {
				t.Errorf("read-only login ran %q", stmt)
			}
		}
		_ = c.Close(ctx)
	}
	// Tables the app creates later are readable too (default privileges).
	if _, err := appConn.Exec(ctx, `CREATE TABLE later (x int); INSERT INTO later VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	held := e.MustConnect(creds.Connection.SessionUrl)
	var x int
	if err := held.QueryRow(ctx, `SELECT x FROM later`).Scan(&x); err != nil || x != 7 {
		t.Fatalf("read a later table: %d %v", x, err)
	}

	// Her login opens no other project's database.
	other := e.CreateProject("Elsewhere")
	sneak := strings.Replace(creds.Connection.SessionUrl, "/"+app.Project.DbName+"?", "/"+other.Project.DbName+"?", 1)
	if c, err := e.Connect(sneak); err == nil {
		err = c.QueryRow(ctx, `SELECT 1`).Scan(&x)
		_ = c.Close(ctx)
		if err == nil {
			t.Error("carol's login opened another project's database")
		}
	}

	// The console is read-only for carol, and project admin actions refuse.
	var res gen.SqlResult
	if code := carol.Do("POST", "/api/v1/projects/"+pid+"/sql", gen.SqlRequest{Query: "DELETE FROM orders", QueryId: uuid.New()}, &res); code != http.StatusForbidden {
		t.Fatalf("read/write console as read-only: %d", code)
	}
	ro := true
	if code := carol.Do("POST", "/api/v1/projects/"+pid+"/sql", gen.SqlRequest{Query: "SELECT count(*) FROM orders", QueryId: uuid.New(), ReadOnly: &ro}, &res); code != http.StatusOK ||
		res.Error != nil || res.Results[0].Rows[0][0] == nil || *res.Results[0].Rows[0][0] != "1" {
		t.Fatalf("read-only console: %d %+v", code, res)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/projects/" + pid + "/rotate-password"},
		{"POST", "/api/v1/projects/" + pid + "/backups"},
		{"PATCH", "/api/v1/projects/" + pid + "/settings"},
		{"POST", "/api/v1/projects/" + pid + "/members"},
		{"GET", "/api/v1/orgs/" + e.OrgID.String() + "/audit"},
	} {
		if code := carol.Do(c.method, c.path, map[string]any{}, nil); code != http.StatusForbidden {
			t.Errorf("carol %s %s: %d, want 403", c.method, c.path, code)
		}
	}

	// Removing carol from the org revokes everything immediately.
	if code := e.Do("DELETE", "/api/v1/orgs/"+e.OrgID.String()+"/members/"+carol.UserID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove carol: %d", code)
	}
	if err := held.QueryRow(ctx, `SELECT 1`).Scan(&x); err == nil {
		t.Error("carol's open connection survived her removal")
	}
	for _, url := range []string{creds.Connection.SessionUrl, creds.Connection.PooledUrl} {
		if c, err := e.Connect(url); err == nil {
			err = c.QueryRow(ctx, `SELECT 1`).Scan(&x)
			_ = c.Close(ctx)
			if err == nil {
				t.Errorf("carol's credentials still work through %s", url)
			}
		}
	}
	admin := e.SharedAdmin("postgres")
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, creds.Role).Scan(&n); err != nil || n != 0 {
		t.Errorf("carol's role is still there: %d %v", n, err)
	}
	if code := carol.Do("GET", "/api/v1/projects/"+pid, nil, nil); code != http.StatusNotFound {
		t.Errorf("carol still sees the project: %d", code)
	}
	if code := carol.Do("GET", "/api/v1/orgs/"+e.OrgID.String(), nil, nil); code != http.StatusNotFound {
		t.Errorf("carol still sees the org: %d", code)
	}
	// The app's own password is untouched.
	if err := appConn.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("app after carol left: %d %v", n, err)
	}

	// Removal and access changes are in the org's audit log.
	var audit gen.AuditList
	if code := e.Do("GET", "/api/v1/orgs/"+e.OrgID.String()+"/audit?limit=200", nil, &audit); code != http.StatusOK {
		t.Fatalf("org audit: %d", code)
	}
	seen := map[string]bool{}
	for _, a := range audit.Items {
		seen[a.Action] = true
	}
	for _, want := range []string{"org.member.invite", "org.member.remove", "project.credentials"} {
		if !seen[want] {
			t.Errorf("org audit has no %s (has %v)", want, seen)
		}
	}
}

// TestDeveloperMemberWritesAsTheProject: a developer's login writes as the
// project owner, so what they create belongs to the app, and dropping them
// from the project leaves it in place.
func TestDeveloperMemberWritesAsTheProject(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	app := e.CreateProject("Blog")
	pid := app.Project.Id.String()

	// Dave is invited through the project's Members tab.
	var added gen.ProjectMemberAdded
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/members", map[string]any{"email": "dave@example.com", "role": "developer"}, &added); code != http.StatusOK || added.Added || added.Invitation == nil {
		t.Fatalf("invite dave: %d %+v", code, added)
	}
	dave := e.AcceptInvitation(e.MailToken("dave@example.com", "invitation"), "dave@example.com")
	var creds gen.PersonalCredentials
	if code := dave.Do("POST", "/api/v1/projects/"+pid+"/credentials", nil, &creds); code != http.StatusOK || creds.Access != gen.PersonalCredentialsAccessReadWrite {
		t.Fatalf("dave's credentials: %d %+v", code, creds)
	}
	c := e.MustConnect(creds.Connection.SessionUrl)
	if _, err := c.Exec(ctx, `CREATE TABLE posts (id int); INSERT INTO posts VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := c.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE tablename = 'posts'`).Scan(&owner); err != nil || owner != app.Project.OwnerRole {
		t.Fatalf("posts belongs to %q (%v), want the project owner", owner, err)
	}
	_ = c.Close(ctx)

	// Made read-only, his login follows.
	if code := e.Do("PATCH", "/api/v1/projects/"+pid+"/members/"+dave.UserID.String(), map[string]string{"role": "read_only"}, nil); code != http.StatusNoContent {
		t.Fatalf("make dave read-only: %d", code)
	}
	c = e.MustConnect(creds.Connection.SessionUrl)
	if _, err := c.Exec(ctx, `INSERT INTO posts VALUES (2)`); err == nil {
		t.Error("dave still writes after becoming read-only")
	}
	_ = c.Close(ctx)

	// Dropped from the project (still in the org): his login goes, the
	// table stays.
	if code := e.Do("DELETE", "/api/v1/projects/"+pid+"/members/"+dave.UserID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove dave: %d", code)
	}
	if c, err := e.Connect(creds.Connection.SessionUrl); err == nil {
		var one int
		err = c.QueryRow(ctx, `SELECT 1`).Scan(&one)
		_ = c.Close(ctx)
		if err == nil {
			t.Error("dave's login survived")
		}
	}
	var roles int
	if err := e.SharedAdmin("postgres").QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, creds.Role).Scan(&roles); err != nil || roles != 0 {
		t.Errorf("dave's role is still there: %d %v", roles, err)
	}
	appConn := e.MustConnect(app.Connection.SessionUrl)
	var n int
	if err := appConn.QueryRow(ctx, `SELECT count(*) FROM posts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("posts after dave left: %d %v", n, err)
	}
	if code := dave.Do("GET", "/api/v1/projects/"+pid, nil, nil); code != http.StatusNotFound {
		t.Errorf("dave still sees the project: %d", code)
	}
	if code := dave.Do("GET", "/api/v1/orgs/"+e.OrgID.String(), nil, nil); code != http.StatusOK {
		t.Errorf("dave is still an org member: %d", code)
	}
}

// TestOrgMembershipRules covers owners, leaving, transfers, and the
// members-can-create-projects setting.
func TestOrgMembershipRules(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	var org gen.Org
	if code := e.Do("POST", "/api/v1/orgs", map[string]string{"name": "Acme"}, &org); code != http.StatusCreated || org.Role != gen.OrgRoleOwner {
		t.Fatalf("create org: %d %+v", code, org)
	}
	oid := org.Id.String()
	invite := func(email, role string) *testenv.Client {
		t.Helper()
		if code := e.Do("POST", "/api/v1/orgs/"+oid+"/members", map[string]string{"email": email, "role": role}, nil); code != http.StatusCreated {
			t.Fatalf("invite %s: %d", email, code)
		}
		return e.AcceptInvitation(e.MailToken(email, "invitation"), email)
	}
	erin := invite("erin@example.com", "admin")
	frank := invite("frank@example.com", "member")

	// Members create projects by default and become their admin.
	p := frank.CreateProject("Frank's app", org.Id)
	var got gen.Project
	if code := frank.Do("GET", "/api/v1/projects/"+p.Project.Id.String(), nil, &got); code != http.StatusOK || got.MyRole == nil || *got.MyRole != gen.ProjectRoleAdmin {
		t.Fatalf("frank's project: %d %+v", code, got.MyRole)
	}
	off := false
	if code := erin.Do("PATCH", "/api/v1/orgs/"+oid, map[string]any{"members_can_create_projects": off}, nil); code != http.StatusOK {
		t.Fatalf("turn off member projects: %d", code)
	}
	if code := frank.Do("POST", "/api/v1/projects", map[string]any{"name": "Another", "org_id": org.Id}, nil); code != http.StatusForbidden {
		t.Fatalf("member creates with the setting off: %d", code)
	}

	// Admins can't touch owners; the last owner can't leave or be demoted.
	if code := erin.Do("PATCH", "/api/v1/orgs/"+oid+"/members/"+frank.UserID.String(), map[string]string{"role": "owner"}, nil); code != http.StatusForbidden {
		t.Fatalf("admin makes an owner: %d", code)
	}
	var me gen.User
	e.Do("GET", "/api/v1/me", nil, &me)
	if code := erin.Do("DELETE", "/api/v1/orgs/"+oid+"/members/"+me.Id.String(), nil, nil); code != http.StatusForbidden {
		t.Fatalf("admin removes the owner: %d", code)
	}
	if code := e.Do("POST", "/api/v1/orgs/"+oid+"/leave", nil, nil); code != http.StatusConflict {
		t.Fatalf("last owner leaves: %d", code)
	}
	if code := e.Do("PATCH", "/api/v1/orgs/"+oid+"/members/"+me.Id.String(), map[string]string{"role": "member"}, nil); code != http.StatusConflict {
		t.Fatalf("last owner demoted: %d", code)
	}
	if code := e.Do("POST", "/api/v1/orgs/"+e.OrgID.String()+"/leave", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("leaving the personal org: %d", code)
	}

	// Transfer ownership to erin, then leave.
	e.Reauth()
	if code := e.Do("POST", "/api/v1/orgs/"+oid+"/transfer-ownership", map[string]string{"user_id": erin.UserID.String()}, nil); code != http.StatusNoContent {
		t.Fatalf("transfer ownership: %d", code)
	}
	var members gen.OrgMemberList
	erin.Do("GET", "/api/v1/orgs/"+oid+"/members", nil, &members)
	roles := map[string]gen.OrgRole{}
	for _, m := range members.Items {
		roles[m.Email] = m.Role
	}
	if roles["erin@example.com"] != gen.OrgRoleOwner || roles[testenv.OwnerEmail] != gen.OrgRoleAdmin {
		t.Fatalf("roles after the transfer: %v", roles)
	}
	if code := e.Do("POST", "/api/v1/orgs/"+oid+"/leave", nil, nil); code != http.StatusNoContent {
		t.Fatalf("leave: %d", code)
	}
	if code := e.Do("GET", "/api/v1/orgs/"+oid, nil, nil); code != http.StatusNotFound {
		t.Fatalf("org after leaving: %d", code)
	}

	// Pending invitations can be revoked; a revoked link is dead.
	var inv gen.InvitationCreated
	if code := erin.Do("POST", "/api/v1/orgs/"+oid+"/members", map[string]string{"email": "gina@example.com", "role": "member"}, &inv); code != http.StatusCreated {
		t.Fatalf("invite gina: %d", code)
	}
	token := e.MailToken("gina@example.com", "invitation")
	if code := erin.Do("DELETE", "/api/v1/orgs/"+oid+"/invitations/"+inv.Invitation.Id.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if code := erin.Do("POST", "/api/v1/invitations/preview", map[string]string{"token": token}, nil); code != http.StatusNotFound {
		t.Fatalf("preview a revoked invitation: %d", code)
	}
}

// TestProjectTransfer moves a project between two organisations the
// caller owns; its members come along and their logins keep working.
func TestProjectTransfer(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	app := e.CreateProject("Movable")
	pid := app.Project.Id.String()
	var org gen.Org
	if code := e.Do("POST", "/api/v1/orgs", map[string]string{"name": "Team"}, &org); code != http.StatusCreated {
		t.Fatalf("create org: %d", code)
	}
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/members", map[string]any{"email": "hank@example.com", "role": "read_only"}, nil); code != http.StatusOK {
		t.Fatalf("invite hank: %d", code)
	}
	hank := e.AcceptInvitation(e.MailToken("hank@example.com", "invitation"), "hank@example.com")
	var creds gen.PersonalCredentials
	if code := hank.Do("POST", "/api/v1/projects/"+pid+"/credentials", nil, &creds); code != http.StatusOK {
		t.Fatalf("hank's credentials: %d", code)
	}
	e.Advance(15 * time.Minute) // past the step-up window of the last sign-in
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/transfer", map[string]string{"org_id": org.Id.String()}, nil); code != http.StatusForbidden {
		t.Fatalf("transfer without step-up auth: %d", code)
	}
	e.Reauth()
	var moved gen.Project
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/transfer", map[string]string{"org_id": org.Id.String()}, &moved); code != http.StatusOK || moved.OrgId != org.Id {
		t.Fatalf("transfer: %d %+v", code, moved)
	}
	var list gen.ProjectList
	hank.Do("GET", "/api/v1/projects?org="+org.Id.String(), nil, &list)
	if len(list.Items) != 1 || list.Items[0].Id != app.Project.Id {
		t.Fatalf("hank's view of the new org: %+v", list)
	}
	c := e.MustConnect(creds.Connection.SessionUrl)
	var one int
	if err := c.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("hank's login after the transfer: %v", err)
	}
	_ = c.Close(ctx)
	// The connection string did not change.
	appConn := e.MustConnect(app.Connection.PooledUrl)
	_ = appConn.Close(ctx)
}

// TestMemberLoginsSurviveRestoreAndPromotion: personal logins keep working
// across an in-place restore and a promotion to a dedicated instance
// (V2 §3.5), with read-only still read-only.
func TestMemberLoginsSurviveRestoreAndPromotion(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	app := e.CreateProject("Durable")
	pid := app.Project.Id.String()
	appConn := e.MustConnect(app.Connection.SessionUrl)
	if _, err := appConn.Exec(ctx, `CREATE TABLE kv (k text PRIMARY KEY, v text); INSERT INTO kv VALUES ('a', '1')`); err != nil {
		t.Fatal(err)
	}
	_ = appConn.Close(ctx)

	member := func(email, role string) gen.PersonalCredentials {
		t.Helper()
		if code := e.Do("POST", "/api/v1/projects/"+pid+"/members", map[string]any{"email": email, "role": role}, nil); code != http.StatusOK {
			t.Fatalf("invite %s: %d", email, code)
		}
		c := e.AcceptInvitation(e.MailToken(email, "invitation"), email)
		var creds gen.PersonalCredentials
		if code := c.Do("POST", "/api/v1/projects/"+pid+"/credentials", nil, &creds); code != http.StatusOK {
			t.Fatalf("credentials for %s: %d", email, code)
		}
		return creds
	}
	reader := member("rita@example.com", "read_only")
	writer := member("walt@example.com", "developer")

	check := func(when string) {
		t.Helper()
		for _, url := range []string{reader.Connection.SessionUrl, reader.Connection.PooledUrl} {
			c := e.MustConnect(url)
			var v string
			if err := c.QueryRow(ctx, `SELECT v FROM kv WHERE k = 'a'`).Scan(&v); err != nil || v != "1" {
				t.Fatalf("%s: reader through %s: %q %v", when, url, v, err)
			}
			if _, err := c.Exec(ctx, `INSERT INTO kv VALUES ('r', 'x')`); err == nil {
				t.Fatalf("%s: the reader wrote", when)
			}
			_ = c.Close(ctx)
		}
		c := e.MustConnect(writer.Connection.PooledUrl)
		if _, err := c.Exec(ctx, `INSERT INTO kv VALUES ($1, 'w') ON CONFLICT (k) DO UPDATE SET v = 'w'`, when); err != nil {
			t.Fatalf("%s: the writer: %v", when, err)
		}
		_ = c.Close(ctx)
	}
	check("before")

	// In place restore.
	op := backupNow(t, e, pid)
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var backups gen.BackupList
	e.Do("GET", "/api/v1/backups?project_id="+pid, nil, &backups)
	if len(backups.Items) == 0 {
		t.Fatal("no backup")
	}
	e.Reauth()
	inPlace, confirm := gen.InPlace, "Durable"
	var rr gen.RestoreResponse
	if code := e.Do("POST", "/api/v1/backups/"+backups.Items[0].Id.String()+"/restore", gen.RestoreRequest{Mode: &inPlace, Confirm: &confirm}, &rr); code != http.StatusAccepted {
		t.Fatalf("restore: %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("restore: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	check("after-restore")

	// Promotion.
	profile, vol := "small", 5
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/promote", gen.PromoteRequest{Profile: &profile, VolumeGb: &vol}, &op); code != http.StatusAccepted {
		t.Fatalf("promote: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("promote: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	check("after-promotion")
	c := e.MustConnect(writer.Connection.SessionUrl)
	var v string
	if err := c.QueryRow(ctx, `SELECT v FROM kv WHERE k = 'after-restore'`).Scan(&v); err != nil || v != "w" {
		t.Fatalf("a write from before the promotion: %q %v", v, err)
	}
	_ = c.Close(ctx)
}
