package backup

import (
	"slices"
	"testing"
)

func TestRelevantWarnings(t *testing.T) {
	lines := []string{
		`pg_restore: error: could not execute query: ERROR:  schema "public" already exists`,
		`Command was: CREATE SCHEMA public;`,
		`pg_restore: error: could not execute query: ERROR:  relation "auth.users" does not exist`,
		`Command was: ALTER TABLE ONLY public.profiles ADD CONSTRAINT profiles_id_fkey FOREIGN KEY (id) REFERENCES auth.users(id);`,
		`pg_restore: warning: errors ignored on restore: 2`,
	}
	got := relevantWarnings(lines, []string{"public"})
	want := []string{`ERROR:  relation "auth.users" does not exist (in: ALTER TABLE ONLY public.profiles ADD CONSTRAINT profiles_id_fkey FOREIGN KEY (id) REFERENCES auth.users(id);)`}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}
