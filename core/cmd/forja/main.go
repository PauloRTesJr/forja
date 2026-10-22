// Command forja is the core: a daemon for the desktop app and a CLI for scripts and CI.
package main

import (
	"os"

	"forja/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.1.0-dev"

func main() { os.Exit(cli.Execute(version)) }
