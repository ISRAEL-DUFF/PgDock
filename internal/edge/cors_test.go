package edge

import (
	"strings"
	"testing"
)

// Every header a Xenibase or Supabase client sends must be allowed in a
// preflight: x-client-info wasn't, and every browser app failed on its first
// request.
func TestAllowHeadersAcceptsWhatClientsSend(t *testing.T) {
	sent := "apikey,authorization,content-type,x-client-info,idempotency-key,prefer,x-upsert,x-metadata,read-replica," +
		"x-supabase-api-version,accept-profile,content-profile,x-some-future-header"
	got := ", " + allowHeaders(sent) + ", "
	for _, h := range strings.Split(sent, ",") {
		if !strings.Contains(got, ", "+h+", ") {
			t.Errorf("%q isn't allowed: %s", h, got)
		}
	}
}

func TestAllowHeadersWithoutARequestIsTheStandardList(t *testing.T) {
	if got := allowHeaders(""); got != corsHeaders {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(corsHeaders, "x-client-info") {
		t.Error("the standard list has no x-client-info")
	}
}

func TestAllowHeadersRefusesAnythingThatIsntAHeaderName(t *testing.T) {
	for _, bad := range []string{"x-a\r\nSet-Cookie: x=1", "bad header", "a;b", "x-é", strings.Repeat("x", 65)} {
		got := allowHeaders("x-ok, " + bad)
		if strings.Contains(got, "\n") || strings.Contains(got, "bad header") || strings.Contains(got, ";") || strings.Contains(got, "é") || strings.Contains(got, strings.Repeat("x", 65)) {
			t.Errorf("%q was echoed: %q", bad, got)
		}
		if !strings.Contains(got, "x-ok") {
			t.Errorf("a good name next to %q was lost: %q", bad, got)
		}
	}
}

func TestAllowHeadersIsBounded(t *testing.T) {
	var asked []string
	for i := 0; i < 500; i++ {
		asked = append(asked, "x-h"+strings.Repeat("a", i%30))
	}
	if n := len(strings.Split(allowHeaders(strings.Join(asked, ",")), ", ")); n > 80 {
		t.Errorf("%d headers allowed from one preflight", n)
	}
}
