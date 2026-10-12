package services

import (
	"slices"
	"testing"

	"github.com/israel-duff/pgdock/internal/store"
)

func TestRewriteExpr(t *testing.T) {
	for _, c := range []struct {
		in, want string
		kind     rewriteKind
		problem  bool
	}{
		{in: "(auth.uid() = user_id)", want: "(pgd_auth.uid() = user_id)"},
		{in: "(( SELECT auth.uid() AS uid) = user_id)", want: "(( SELECT pgd_auth.uid() AS uid) = user_id)"},
		{in: "(auth.role() = 'authenticated'::text)", want: "(pgd_auth.role() = 'user'::text)"},
		{in: "((auth.jwt() ->> 'role'::text) = 'service_role'::text)", want: "((pgd_auth.claims() ->> 'role'::text) = 'service'::text)"},
		{in: "(email = auth.email())", want: "(email = (pgd_auth.claims() ->> 'email'))"},
		// A literal that only looks like a role stays when nothing compares roles.
		{in: "(kind = 'authenticated'::text)", want: "(kind = 'authenticated'::text)"},
		// Literals and quoted identifiers are left alone.
		{in: `("auth.uid()" = 'auth.uid()'::text)`, want: `("auth.uid()" = 'auth.uid()'::text)`},
		{in: "(EXISTS ( SELECT 1 FROM auth.users u WHERE (u.id = auth.uid())))", problem: true},
		{in: "(bucket_id = 'avatars'::text)", kind: rewriteStorage, want: "(bucket = 'avatars'::text)"},
		{
			in:   "((bucket_id = 'avatars'::text) AND ((storage.foldername(name))[1] = (auth.uid())::text))",
			kind: rewriteStorage,
			want: "((bucket = 'avatars'::text) AND ((pgd_storage.foldername(path))[1] = (pgd_auth.uid())::text))",
		},
		{in: "(owner_id = (auth.uid())::text)", kind: rewriteStorage, want: "((owner)::text = (pgd_auth.uid())::text)"},
		{in: "(storage.extension(name) = 'jpg'::text)", kind: rewriteStorage, want: "(pgd_storage.extension(path) = 'jpg'::text)"},
		{in: "((metadata ->> 'size'::text))::int < 100", kind: rewriteStorage, problem: true},
		// The literal 'name' is not the column.
		{in: "(user_metadata ->> 'name'::text) = 'x'", kind: rewriteStorage, want: "(user_metadata ->> 'name'::text) = 'x'"},
		{in: "(storage.can_access(name))", kind: rewriteStorage, problem: true},
	} {
		got, problems := rewriteExpr(c.in, c.kind)
		if c.problem {
			if len(problems) == 0 {
				t.Errorf("%s: no problem reported (got %s)", c.in, got)
			}
			continue
		}
		if len(problems) > 0 || got != c.want {
			t.Errorf("%s:\n got  %s %v\n want %s", c.in, got, problems, c.want)
		}
	}
}

func TestMapPolicyRoles(t *testing.T) {
	p := store.Project{DbName: "db_x"}
	got := mapPolicyRoles(p, []string{"authenticated", "anon", "public", "service_role"})
	want := []string{store.UserRole("db_x"), store.AnonRole("db_x"), "public", store.ServiceRole("db_x")}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if usesSupabaseRoles([]string{"public"}) || !usesSupabaseRoles([]string{"public", "anon"}) {
		t.Fatal("usesSupabaseRoles")
	}
}

func TestSupabasePhone(t *testing.T) {
	for in, want := range map[string]string{"2348012345678": "+2348012345678", "+14155550100": "+14155550100", "": "", " ": ""} {
		if got := supabasePhone(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
