// Package version exposes build information injected at link time.
//
//	go build -ldflags "-X github.com/israel-duff/pgdock/internal/version.Version=1.2.3 ..."
package version

import "runtime"

// Set via -ldflags by `make build`. The defaults describe a plain `go build`.
var (
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info is the build information of the running binary.
type Info struct {
	Version   string
	Commit    string
	BuildDate string
	GoVersion string
}

// Get returns the build information of the running binary.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
	}
}
