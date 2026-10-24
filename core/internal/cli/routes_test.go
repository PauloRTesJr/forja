package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"forja/internal/api"
)

// appOnly are the endpoints only the desktop app needs, each with the reason. Everything
// else the core serves must be reachable from a command (docs/decisions.md, decision 18).
var appOnly = map[string]string{
	"GET /v1/providers/{id}/thumbnail": "relays catalog pictures to the app's page, which loads images only from the core",
	"PUT /v1/assets/{id}/thumbnail":    "stores the thumbnail the app's 3D viewer renders",
	"POST /v1/artifacts/import":        "copies a picked image into the store so the app can preview it; the CLI passes paths",
	"PUT /v1/artifacts":                "uploads bytes for a client that cannot hand the core a path; the CLI runs beside it",
	"GET /v1/events/stream":            "the GET /v1/events stream as server-sent events, for Electron's main process",
}

func commandRoutes(cmd *cobra.Command, into map[string][]string) {
	if routes := cmd.Annotations[routesKey]; routes != "" {
		for _, r := range strings.Split(routes, "\n") {
			into[r] = append(into[r], cmd.CommandPath())
		}
	}
	for _, sub := range cmd.Commands() {
		commandRoutes(sub, into)
	}
}

func TestEveryEndpointIsReachableFromTheCommandLine(t *testing.T) {
	covered := map[string][]string{}
	commandRoutes((&app{}).root(), covered)
	all := api.Routes()
	for _, r := range all {
		if _, ok := appOnly[r]; ok {
			if len(covered[r]) > 0 {
				t.Errorf("%s is listed as app-only, yet %v call it: take it off the list", r, covered[r])
			}
			continue
		}
		if len(covered[r]) == 0 {
			t.Errorf("no command calls %s: add one, or list it in appOnly with the reason only the app needs it", r)
		}
	}
	for r, cmds := range covered {
		if !slices.Contains(all, r) {
			t.Errorf("%v say they call %s, which the core does not serve", cmds, r)
		}
	}
	for r := range appOnly {
		if !slices.Contains(all, r) {
			t.Errorf("appOnly lists %s, which the core does not serve", r)
		}
	}
}
