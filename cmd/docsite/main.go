// Command docsite builds the documentation site from docs/ (see
// internal/docsite): pgdock-docsite -src docs -out dist/docs. It fails
// when a link between pages is broken.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/israel-duff/pgdock/internal/docsite"
)

func main() {
	src := flag.String("src", "docs", "the docs directory (with site.json)")
	out := flag.String("out", "dist/docs", "where the site goes")
	flag.Parse()
	broken, err := docsite.Build(*src, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docsite:", err)
		os.Exit(1)
	}
	for _, b := range broken {
		fmt.Fprintln(os.Stderr, "broken link:", b)
	}
	if len(broken) > 0 {
		os.Exit(1)
	}
	fmt.Printf("docs site written to %s\n", *out)
}
