// Command fakehook is a webhook receiver for the e2e bundle. It records
// every request it gets and lists them as JSON at GET /requests, so browser
// tests can check what PGDock sent (and its signature).
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// Request is one received request.
type Request struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Header     map[string]string `json:"header"`
	Body       string            `json:"body"`
	ReceivedAt time.Time         `json:"received_at"`
}

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	flag.Parse()
	var mu sync.Mutex
	var got []Request
	mux := http.NewServeMux()
	mux.HandleFunc("GET /requests", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		out := got
		if out == nil {
			out = []Request{}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		h := map[string]string{}
		for k := range r.Header {
			h[k] = r.Header.Get(k)
		}
		mu.Lock()
		got = append(got, Request{Method: r.Method, Path: r.URL.Path, Header: h, Body: string(body), ReceivedAt: time.Now()})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	log.Printf("fakehook listening on %s", *listen)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
