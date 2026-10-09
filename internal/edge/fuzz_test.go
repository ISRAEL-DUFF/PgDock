package edge

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/jwtes"
)

// The V4-M37 security review's fuzz tests of the edge's isolation (V4 §13):
// a request is served as a project only when its host names that project,
// its key is one of that project's live keys, and its user token (if any)
// was signed by that project for that project. `go test -fuzz` runs them
// further; plain `go test` runs the seeds.

const fuzzDomain = "api.fuzz.test"

type fuzzProject struct {
	ref      string
	pub, sec string
	der      []byte
	kid      string
	tokens   []string // valid user tokens for this project
	foreign  string   // signed with this project's key for the other's audience
	unsigned string   // its signature cut off
	expired  string
	service  string // role service, which a user token can't claim
}

func newFuzzProject(t testing.TB, ref string) (*fuzzProject, edgeapi.Project) {
	t.Helper()
	// Both projects use the same kid, so a token can't pick the other's
	// key by naming it.
	der, jwk, err := jwtes.Generate("k1")
	if err != nil {
		t.Fatal(err)
	}
	fp := &fuzzProject{ref: ref, pub: "pgd_pub_" + ref, sec: "pgd_sec_" + ref, der: der, kid: "k1"}
	sign := func(c map[string]any) string {
		s, err := jwtes.Sign(der, "k1", c)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	exp := float64(time.Now().Add(time.Hour).Unix())
	fp.tokens = []string{sign(map[string]any{"sub": uuid.NewString(), "role": "user", "aud": ref, "exp": exp})}
	fp.expired = sign(map[string]any{"sub": uuid.NewString(), "role": "user", "aud": ref, "exp": float64(time.Now().Add(-time.Hour).Unix())})
	fp.service = sign(map[string]any{"role": "service", "aud": ref, "exp": exp})
	fp.unsigned = strings.Join(strings.Split(fp.tokens[0], ".")[:2], ".") + "."
	raw, _ := json.Marshal(jwk)
	pc := edgeapi.Project{Ref: ref, ProjectID: uuid.New(), OrgID: uuid.New(), State: edgeapi.StateActive, Version: 1,
		Keys: []edgeapi.Key{{ID: uuid.New(), Kind: edgeapi.KindPublishable, Hash: edgeapi.HashKey(fp.pub)},
			{ID: uuid.New(), Kind: edgeapi.KindSecret, Hash: edgeapi.HashKey(fp.sec)}},
		JWKs: []json.RawMessage{raw}}
	return fp, pc
}

func FuzzGatewayIsolation(f *testing.F) {
	a, pa := newFuzzProject(f, "aaaaaaa1")
	b, pb := newFuzzProject(f, "bbbbbbb2")
	a.foreign, _ = jwtes.Sign(a.der, a.kid, map[string]any{"sub": uuid.NewString(), "role": "user", "aud": b.ref,
		"exp": float64(time.Now().Add(time.Hour).Unix())})
	b.foreign, _ = jwtes.Sign(b.der, b.kid, map[string]any{"sub": uuid.NewString(), "role": "user", "aud": a.ref,
		"exp": float64(time.Now().Add(time.Hour).Unix())})
	e := New(Config{Domain: fuzzDomain})
	e.apply([]edgeapi.Project{pa, pb})
	e.ready = true
	projects := map[string]*fuzzProject{a.ref: a, b.ref: b}
	keys := []string{"", a.pub, a.sec, b.pub, b.sec}
	tokens := []string{"", a.tokens[0], b.tokens[0], a.foreign, b.foreign, a.unsigned, a.expired, a.service}

	for _, host := range []string{a.ref + "." + fuzzDomain, b.ref + "." + fuzzDomain, "AAAAAAA1." + fuzzDomain + ":443",
		a.ref + "." + fuzzDomain + ".", b.ref + "." + a.ref + "." + fuzzDomain, a.ref + "." + fuzzDomain + ".evil", fuzzDomain, ""} {
		for k := range keys {
			for tk := range tokens {
				f.Add(host, uint8(k), uint8(tk), "", "")
			}
		}
	}
	f.Add(a.ref+"."+fuzzDomain, uint8(255), uint8(255), b.pub+" ", "Bearer "+a.tokens[0])
	f.Add(a.ref+"."+fuzzDomain, uint8(1), uint8(255), "", a.tokens[0][:len(a.tokens[0])-2]+"AA")

	f.Fuzz(func(t *testing.T, host string, keySel, tokSel uint8, rawKey, rawToken string) {
		key := rawKey
		if int(keySel) < len(keys) {
			key = keys[keySel]
		}
		token := rawToken
		if int(tokSel) < len(tokens) {
			token = tokens[tokSel]
		}
		r := httptest.NewRequest(http.MethodGet, "http://edge/fuzz/v1/x", nil)
		r.Host = host
		if key != "" {
			if !validHeader(key) {
				return
			}
			r.Header.Set("apikey", key)
		}
		if token != "" {
			if !validHeader(token) {
				return
			}
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		var body struct {
			Error Error `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		served := w.Code == http.StatusNotFound && body.Error.Code == "no_such_endpoint"

		// What it should be, worked out independently of the gateway.
		h := strings.ToLower(host)
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		h = strings.TrimSuffix(h, ".")
		p := projects[strings.TrimSuffix(h, "."+fuzzDomain)]
		if !strings.HasSuffix(h, "."+fuzzDomain) {
			p = nil
		}
		want := false
		if p != nil {
			switch key {
			case p.sec:
				want = true
			case p.pub:
				tok := strings.TrimSpace(token)
				want = tok == "" || tok == p.tokens[0]
			}
		}
		if served && !want {
			t.Fatalf("served host %q key %q token %q (%d %+v)", host, key, token, w.Code, body.Error)
		}
		if want && !served && p != nil && token == strings.TrimSpace(token) {
			t.Fatalf("refused host %q key %q token %q: %d %+v", host, key, token, w.Code, body.Error)
		}
	})
}

// validHeader: what net/http would send as a header value.
func validHeader(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

func FuzzRefFromHost(f *testing.F) {
	e := New(Config{Domain: "api.pgdock.ng."})
	for _, s := range []string{"k7f3m2q9.api.pgdock.ng", "K7F3M2Q9.API.pgdock.ng:443", "a.k7f3m2q9.api.pgdock.ng", "api.pgdock.ng",
		".api.pgdock.ng", "k7f3m2q9.api.pgdock.ng.evil", "[::1]:80", "k7f3m2q9.api.pgdock.ng:", "x.api.pgdock.ng.."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, host string) {
		ref, ok := e.refFromHost(host)
		if !ok {
			return
		}
		if ref == "" || strings.ContainsAny(ref, ".:/[]") || ref != strings.ToLower(ref) {
			t.Fatalf("%q gave ref %q", host, ref)
		}
		if !strings.Contains(strings.ToLower(host), ref+".api.pgdock.ng") {
			t.Fatalf("%q gave ref %q not under the domain", host, ref)
		}
	})
}

var sqlWord = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// FuzzReadSQL: whatever a client sends as a query, the SQL the edge
// builds quotes every name and passes every value as a parameter. Outside
// quoted identifiers and literals there are only the builder's own words
// (those in the package's source), the catalog's types, table aliases and
// numbers, and never
// a statement separator or comment.
func FuzzReadSQL(f *testing.F) {
	allowed := map[string]bool{}
	files, err := filepath.Glob("*.go")
	if err != nil {
		f.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		for _, w := range sqlWord.FindAllString(string(src), -1) {
			allowed[strings.ToLower(w)] = true
		}
	}
	// Column types come from the project's catalog, not the request.
	for _, t := range testCatalog().Ordered {
		for _, c := range t.Columns {
			for _, w := range sqlWord.FindAllString(c.Type, -1) {
				allowed[strings.ToLower(w)] = true
			}
		}
	}
	for _, seed := range []string{
		"select=id,title,author(name),comments(body),meta->plan&where=title:ilike:%25go%25&order=created_at:desc&limit=20",
		"select=id&where=title:eq:zzinject'%20OR%20'a'='a",
		"select=meta->>a'b&where=meta->x'y:eq:1",
		`select=id&where=title:in:a,"b,c",zzinject');DROP TABLE x;--`,
		"select=t:title&or=title:eq:a,id:gt:3&order=id:asc",
		"select=editor:authors!editor_id(name)&where=tags:contains:[\"zzinject\"]",
		"select=id&where=created_at:gte:2026-10-01&cursor=eyJ4IjoxfQ",
		"select=*&where=title:is:null&count=exact",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		v, err := url.ParseQuery(raw)
		if err != nil {
			return
		}
		q, err := queryFromURL(v)
		if err != nil {
			return
		}
		cat := testCatalog()
		b := &builder{cat: cat}
		st, err := b.read(cat.Find("posts"), q)
		if err != nil {
			return
		}
		for _, sql := range []string{st.SQL, st.CountSQL} {
			checkSQL(t, raw, sql, allowed)
		}
		for i := range st.Args {
			if !strings.Contains(st.SQL, "$"+strconv.Itoa(i+1)) && !strings.Contains(st.CountSQL, "$"+strconv.Itoa(i+1)) {
				t.Fatalf("%q: argument %d unused:\n%s", raw, i+1, st.SQL)
			}
		}
	})
}

var aliasWord = regexp.MustCompile(`^(t|r|c|k)[0-9]*$`)

func checkSQL(t *testing.T, input, sql string, allowed map[string]bool) {
	t.Helper()
	var outside strings.Builder
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c != '"' && c != '\'' {
			outside.WriteByte(c)
			continue
		}
		// A quoted name or literal ends at a lone closing quote.
		j := i + 1
		for ; j < len(sql); j++ {
			if sql[j] == c {
				if j+1 < len(sql) && sql[j+1] == c {
					j++
					continue
				}
				break
			}
		}
		if j >= len(sql) {
			t.Fatalf("%q: unterminated %c in:\n%s", input, c, sql)
		}
		outside.WriteByte(' ')
		i = j
	}
	out := outside.String()
	for _, bad := range []string{";", "--", "/*", "zzinject"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q: %q outside quotes in:\n%s", input, bad, sql)
		}
	}
	for _, w := range sqlWord.FindAllString(out, -1) {
		if !allowed[strings.ToLower(w)] && !aliasWord.MatchString(w) {
			t.Fatalf("%q: unexpected word %q outside quotes in:\n%s", input, w, sql)
		}
	}
}
