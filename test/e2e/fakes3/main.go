// Command fakes3 is an in-memory S3 server for the e2e bundle (never for
// real data: everything is lost when it stops).
package main

import (
	"flag"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func main() {
	addr := flag.String("listen", ":9000", "listen address")
	buckets := flag.String("buckets", "pgdock", "comma-separated buckets to create")
	flag.Parse()
	be := s3mem.New()
	for _, b := range strings.Split(*buckets, ",") {
		if err := be.CreateBucket(strings.TrimSpace(b)); err != nil {
			log.Fatal(err)
		}
	}
	srv := &http.Server{Addr: *addr, Handler: gofakes3.New(be).Server(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("fakes3 listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
