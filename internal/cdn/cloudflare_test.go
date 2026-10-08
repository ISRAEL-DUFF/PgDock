package cdn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudflarePurge(t *testing.T) {
	var got [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones/z1/purge_cache" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success":false}`))
			return
		}
		var in struct {
			Prefixes []string `json:"prefixes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		got = append(got, in.Prefixes)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	var ps []string
	for i := 0; i < 35; i++ {
		ps = append(ps, fmt.Sprintf("ref.example.com/storage/v1/public/b%d/", i))
	}
	if err := (Cloudflare{ZoneID: "z1", Token: "tok", BaseURL: srv.URL}).PurgePrefixes(context.Background(), ps); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got[0]) != 30 || len(got[1]) != 5 {
		t.Fatalf("batches: %v", got)
	}
	if err := (Cloudflare{ZoneID: "z1", Token: "wrong", BaseURL: srv.URL}).PurgePrefixes(context.Background(), ps[:1]); err == nil {
		t.Fatal("a refused purge succeeded")
	}
}
