package services

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/israel-duff/pgdock/internal/store"
)

// Rewriting Supabase policies and defaults into PGDock's form (V4 §9): the
// helper calls (auth.uid() → pgd_auth.uid() and so on), the role names a
// policy applies to, and the role names expressions compare with. Anything
// else that refers to Supabase's auth or storage schemas can't be mapped
// and is reported instead.

// supabaseRoleMap is what each Supabase role becomes; the request role of
// a project, by the role's claim.
var supabaseRoleClaims = map[string]string{"anon": "anon", "authenticated": "user", "service_role": "service"}

// mapPolicyRoles maps a policy's roles onto p's request roles.
func mapPolicyRoles(p store.Project, roles []string) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		switch r {
		case "anon":
			r = store.AnonRole(p.DbName)
		case "authenticated":
			r = store.UserRole(p.DbName)
		case "service_role":
			r = store.ServiceRole(p.DbName)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// usesSupabaseRoles reports whether roles name a Supabase role.
func usesSupabaseRoles(roles []string) bool {
	for _, r := range roles {
		if _, ok := supabaseRoleClaims[r]; ok {
			return true
		}
	}
	return false
}

// exprSegment is a piece of an SQL expression: code, or a quoted literal
// or identifier, which rewrites leave alone.
type exprSegment struct {
	text   string
	quoted byte // 0 for code, '\'' or '"'
}

// splitExpr splits a deparsed expression into code and quoted segments.
func splitExpr(s string) []exprSegment {
	var out []exprSegment
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\'' && c != '"' {
			continue
		}
		if i > start {
			out = append(out, exprSegment{text: s[start:i]})
		}
		j := i + 1
		for j < len(s) {
			if s[j] == c {
				if j+1 < len(s) && s[j+1] == c { // a doubled quote
					j += 2
					continue
				}
				break
			}
			j++
		}
		end := min(j+1, len(s))
		out = append(out, exprSegment{text: s[i:end], quoted: c})
		i, start = end-1, end
	}
	if start < len(s) {
		out = append(out, exprSegment{text: s[start:]})
	}
	return out
}

func joinExpr(segs []exprSegment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	return b.String()
}

// rewriteKind says what an expression belongs to: a table's policy or
// default, or a policy moving from storage.objects to pgd_storage.objects.
type rewriteKind int

const (
	rewriteTable rewriteKind = iota
	rewriteStorage
)

var (
	reAuthUID     = regexp.MustCompile(`\bauth\s*\.\s*uid\s*\(\s*\)`)
	reAuthRole    = regexp.MustCompile(`\bauth\s*\.\s*role\s*\(\s*\)`)
	reAuthJWT     = regexp.MustCompile(`\bauth\s*\.\s*jwt\s*\(\s*\)`)
	reAuthEmail   = regexp.MustCompile(`\bauth\s*\.\s*email\s*\(\s*\)`)
	reAuthAny     = regexp.MustCompile(`\bauth\s*\.\s*([a-z_][a-z0-9_]*)`)
	reStorageFn   = regexp.MustCompile(`\bstorage\s*\.\s*(foldername|filename|extension)\s*\(`)
	reStorageAny  = regexp.MustCompile(`\bstorage\s*\.\s*([a-z_][a-z0-9_]*)`)
	reWord        = regexp.MustCompile(`(^|[^a-zA-Z0-9_.$])([a-z_][a-z0-9_]*)\b`)
	storageColMap = map[string]string{"bucket_id": "bucket", "name": "path", "owner": "owner", "id": "id",
		"created_at": "created_at", "updated_at": "updated_at", "user_metadata": "user_metadata"}
	// storageNoCol are storage.objects columns PGDock has no equivalent of.
	storageNoCol = map[string]bool{"metadata": true, "path_tokens": true, "version": true, "last_accessed_at": true, "level": true}
)

// rewriteExpr rewrites a deparsed policy or default expression. It returns
// the new expression and what it couldn't rewrite (the expression is only
// usable when that's empty).
func rewriteExpr(expr string, kind rewriteKind) (string, []string) {
	if expr == "" {
		return "", nil
	}
	segs := splitExpr(expr)
	var problems []string
	note := func(f string, a ...any) {
		m := fmt.Sprintf(f, a...)
		if !slices.Contains(problems, m) {
			problems = append(problems, m)
		}
	}
	roleCompare := false
	for i, s := range segs {
		if s.quoted != 0 {
			if s.quoted == '\'' && s.text == "'role'" {
				roleCompare = true
			}
			if s.quoted == '"' && kind == rewriteStorage {
				// A quoted column of storage.objects.
				name := strings.ReplaceAll(strings.Trim(s.text, `"`), `""`, `"`)
				if storageNoCol[name] {
					note("uses storage.objects.%s, which PGDock storage has no equivalent of", name)
				} else if to, ok := storageColMap[name]; ok && to != name {
					segs[i].text = to
				}
			}
			continue
		}
		t := s.text
		if reAuthRole.MatchString(t) {
			roleCompare = true
		}
		t = reAuthUID.ReplaceAllString(t, "pgd_auth.uid()")
		t = reAuthRole.ReplaceAllString(t, "pgd_auth.role()")
		t = reAuthJWT.ReplaceAllString(t, "pgd_auth.claims()")
		t = reAuthEmail.ReplaceAllString(t, "(pgd_auth.claims() ->> 'email')")
		for _, m := range reAuthAny.FindAllStringSubmatch(t, -1) {
			note("refers to auth.%s", m[1])
		}
		if kind == rewriteStorage {
			t = reStorageFn.ReplaceAllString(t, "pgd_storage.$1(")
			t = rewriteStorageColumns(t, note)
		}
		for _, m := range reStorageAny.FindAllStringSubmatch(t, -1) {
			note("refers to storage.%s", m[1])
		}
		segs[i].text = t
	}
	if roleCompare {
		for i, s := range segs {
			if s.quoted != '\'' {
				continue
			}
			switch s.text {
			case "'authenticated'":
				segs[i].text = "'user'"
			case "'service_role'":
				segs[i].text = "'service'"
			}
		}
	}
	return joinExpr(segs), problems
}

// rewriteStorageColumns renames storage.objects' columns in code to
// pgd_storage.objects' (a word followed by "(" is a function, not one).
func rewriteStorageColumns(t string, note func(string, ...any)) string {
	var b strings.Builder
	last := 0
	for _, m := range reWord.FindAllStringSubmatchIndex(t, -1) {
		ws, we := m[4], m[5]
		name := t[ws:we]
		to := name
		switch {
		case storageNoCol[name]:
			note("uses storage.objects.%s, which PGDock storage has no equivalent of", name)
		case name == "owner_id":
			to = "(owner)::text"
		case storageColMap[name] != "":
			to = storageColMap[name]
		}
		if to == name || strings.HasPrefix(strings.TrimLeft(t[we:], " "), "(") {
			continue
		}
		b.WriteString(t[last:ws])
		b.WriteString(to)
		last = we
	}
	b.WriteString(t[last:])
	return b.String()
}
