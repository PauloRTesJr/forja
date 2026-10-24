// Command forja is the core: a daemon for the desktop app and a CLI for scripts and CI.
package main

import (
	"os"

	"forja/internal/cli"
)

// version is set at build time from the repository's VERSION file (`make core`).
var version = "dev"

func main() { os.Exit(cli.Execute(version)) }
