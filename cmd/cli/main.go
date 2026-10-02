// Command pgdock is the PGDock command-line tool (V2 §7.1).
package main

import (
	"os"
	"os/exec"
	"runtime"

	"github.com/israel-duff/pgdock/internal/cli"
)

func main() {
	app := &cli.App{OpenBrowser: openBrowser}
	os.Exit(app.Run(os.Args[1:]))
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}
