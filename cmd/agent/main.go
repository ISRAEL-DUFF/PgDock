// Command pgdock-agent runs on each node and executes the server's commands
// (dedicated instances, dumps, WAL-G). Its command API arrives in M4.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/israel-duff/pgdock/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	v := version.Get()
	if *showVersion {
		fmt.Printf("pgdock-agent %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.BuildDate, v.GoVersion)
		return
	}
	fmt.Fprintln(os.Stderr, "pgdock-agent: not implemented yet (see M4 in the build plan); use -version")
	os.Exit(2)
}
