package cli

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/providers"
)

func (a *app) providersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Add and manage other computers that run models for this one",
		Long: "A provider runs stages for this computer: this computer itself (local), another computer\n" +
			"sharing its GPU, or a model hub (forja hubs). Pair another computer with the code its\n" +
			"`forja share code` shows, or give its access token on stdin.",
	}
	cmd.AddCommand(calls(&cobra.Command{
		Use: "ls", Short: "List providers with their health and keys",
		RunE: func(*cobra.Command, []string) error {
			var rows []api.ProviderRow
			if err := a.get("/v1/providers", &rows); err != nil {
				return err
			}
			a.print(rows, func() {
				a.table("ID\tNAME\tKIND\tADDRESS\tHEALTH\tKEY\tCERTIFICATE", func(row func(...any)) {
					for _, r := range rows {
						kind := strings.TrimSpace(r.Kind + " " + r.Vendor)
						if r.Paid {
							kind += " (paid)"
						}
						addr := orDash(r.URL)
						if r.EndpointID != "" {
							addr = "endpoint " + r.EndpointID
						}
						row(r.ID, r.Label, kind, addr, health(r), keyHint(r.Key), orDash(r.Fingerprint))
					}
				})
			})
			return nil
		},
	}, "GET /v1/providers"))

	cmd.AddCommand(calls(&cobra.Command{
		Use: "kinds", Short: "Describe the kinds of provider that can be added",
		RunE: func(*cobra.Command, []string) error {
			var kinds []providers.Kind
			if err := a.get("/v1/provider-kinds", &kinds); err != nil {
				return err
			}
			a.print(kinds, func() {
				for _, k := range kinds {
					name := k.Kind
					if k.Vendor != "" {
						name += " " + k.Vendor
					}
					a.printf("%s: %s\n  %s\n", name, k.Label, k.Description)
				}
			})
			return nil
		},
	}, "GET /v1/provider-kinds"))

	var testToken bool
	var testCloud cloudFlags
	test := calls(&cobra.Command{
		Use:   "test [runpod|runpod-pod|modal|comfyui] <address>",
		Short: "Try a provider's address and keys before adding it",
		Long:  "With only an address, tries another computer. Before an address, the kind of provider:\n" + vendorHelp(),
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			var body any
			if len(args) == 2 {
				testCloud.tokenStdin = testToken
				b, err := a.cloudBody(args[0], args[1], &testCloud)
				if err != nil {
					return err
				}
				body = b
			} else {
				b := map[string]string{"url": args[0]}
				if testToken {
					token, err := a.readSecret("access token")
					if err != nil {
						return err
					}
					b["token"] = token
				}
				body = b
			}
			var probe api.ProbeView
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.Do(context.Background(), "POST", "/v1/providers/test", body, &probe); err != nil {
				return err
			}
			a.print(probe, func() {
				if len(args) == 2 {
					a.showCloudProbe(probe)
				} else {
					a.showProbe(probe)
				}
			})
			return nil
		},
	}, "POST /v1/providers/test")
	test.Flags().BoolVar(&testToken, "token-stdin", false, "also try the access token read from stdin")
	testCloud.register(test)
	cmd.AddCommand(test)

	var label, code string
	var addToken bool
	var addCloud cloudFlags
	add := calls(&cobra.Command{
		Use:   "add [runpod|runpod-pod|modal|comfyui] <address>",
		Short: "Add another computer, a rented GPU or a ComfyUI",
		Long: "Adds another computer that shares its GPU. On it, `forja share on` then `forja share code`\n" +
			"(or the app's Settings) shows the address and a six-digit code:\n" +
			"  forja providers add 192.168.1.20 --code 482913\n" +
			"Without a code, --token-stdin reads its access token from stdin. The token stays in the OS\n" +
			"keychain; config.toml keeps the address and the certificate to trust.\n\n" +
			"Before the address, the kind of provider adds a rented GPU or a ComfyUI instead:\n" + vendorHelp() +
			"For example:\n" +
			"  forja providers add runpod abc123xyz789 --price 0.00031 < runpod-key.txt\n" +
			"  forja providers add comfyui 192.168.1.30:8188 --workflow shape=hunyuan-shape.json\n" +
			"Rented GPUs are paid: stages use them when pinned (forja models pin), or when\n" +
			"routing.allow_paid_fallback is on.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 2 {
				addCloud.label, addCloud.tokenStdin = label, addToken
				body, err := a.cloudBody(args[0], args[1], &addCloud)
				if err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				var added api.AddedProvider
				if err := c.Do(context.Background(), "POST", "/v1/providers", body, &added); err != nil {
					return err
				}
				a.print(added, func() {
					a.printf("Added %s as %s.\n", added.Label, added.ID)
					if added.Probe != nil {
						a.showCloudProbe(*added.Probe)
					}
				})
				return nil
			}
			body := map[string]string{"url": args[0], "label": label}
			switch {
			case code != "" && addToken:
				return errors.New("give --code or --token-stdin, not both")
			case code != "":
				body["code"] = strings.ReplaceAll(code, " ", "")
			case addToken:
				token, err := a.readSecret("access token")
				if err != nil {
					return err
				}
				body["token"] = token
			default:
				return errors.New("give the pairing code the other computer shows (--code), or --token-stdin")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var added api.AddedProvider
			if err := c.Do(context.Background(), "POST", "/v1/providers", body, &added); err != nil {
				return err
			}
			a.print(added, func() {
				a.printf("Added %s as %s.\n", added.Label, added.ID)
				if added.Probe != nil {
					a.showProbe(*added.Probe)
				}
			})
			return nil
		},
	}, "POST /v1/providers")
	add.Flags().StringVar(&label, "label", "", "a name for it (default: the name it gives)")
	add.Flags().StringVar(&code, "code", "", "the six-digit pairing code the other computer shows")
	add.Flags().BoolVar(&addToken, "token-stdin", false, "read its access token from stdin instead")
	addCloud.register(add)
	cmd.AddCommand(add)

	cmd.AddCommand(calls(&cobra.Command{
		Use: "rm <provider>...", Short: "Forget providers: their settings, keys and the stages routed to them",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, id := range args {
				if err := c.Do(context.Background(), "DELETE", "/v1/providers/"+url.PathEscape(id), nil, nil); err != nil {
					return err
				}
			}
			return nil
		},
	}, "DELETE /v1/providers/{id}"))

	cmd.AddCommand(calls(&cobra.Command{
		Use: "token <provider>", Short: "Replace a provider's key or token, read from stdin, once the new one works",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			token, err := a.readSecret("new key or token")
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			return c.Do(context.Background(), "PUT", "/v1/providers/"+url.PathEscape(args[0])+"/token",
				map[string]string{"token": token}, nil)
		},
	}, "PUT /v1/providers/{id}/token"))
	return cmd
}

func (a *app) showProbe(p api.ProbeView) {
	a.printf("  %s  forja-engine %s, %d ms", p.URL, p.Version, p.Latency)
	if p.GPU != "" {
		a.printf(", %s", p.GPU)
	}
	a.printf("\n")
	if p.Fingerprint != "" {
		a.printf("  certificate %s: check that the other computer shows the same\n", p.Fingerprint)
	}
}

func (a *app) shareCmd() *cobra.Command {
	show := func(v api.ShareView) {
		a.print(v, func() {
			switch {
			case v.Pending == "restart":
				a.printf("Saved: it applies once the running jobs finish and the engine restarts.\n")
			case v.Active:
				a.printf("Sharing this computer's GPU on port %d.\n", v.Port)
			case v.Enabled:
				a.printf("Sharing is on; the engine serves the network once it runs (open the app or run forja daemon).\n")
			default:
				a.printf("Not sharing this computer's GPU. Turn it on with forja share on.\n")
			}
			if v.Enabled {
				for _, addr := range v.Addresses {
					a.printf("  address      %s\n", addr)
				}
				if v.Fingerprint != "" {
					a.printf("  certificate  %s\n", v.Fingerprint)
				}
				a.printf("On the other computer: forja providers add <address> --code <code from forja share code>\n")
			}
		})
	}
	cmd := calls(&cobra.Command{
		Use:   "share",
		Short: "Share this computer's GPU with other computers on the network",
		Long: "Without a subcommand, shows whether this computer shares its GPU, and where.\n" +
			"Another computer joins with a pairing code: forja share code.",
		RunE: func(*cobra.Command, []string) error {
			var v api.ShareView
			if err := a.get("/v1/share", &v); err != nil {
				return err
			}
			show(v)
			return nil
		},
	}, "GET /v1/share")
	set := func(on bool) func(*cobra.Command, []string) error {
		return func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var v api.ShareView
			if err := c.Do(context.Background(), "PUT", "/v1/share", map[string]bool{"enabled": on}, &v); err != nil {
				return err
			}
			show(v)
			return nil
		}
	}
	cmd.AddCommand(calls(&cobra.Command{Use: "on", Short: "Share this computer's GPU", RunE: set(true)}, "PUT /v1/share"))
	cmd.AddCommand(calls(&cobra.Command{Use: "off", Short: "Stop sharing this computer's GPU", RunE: set(false)}, "PUT /v1/share"))
	cmd.AddCommand(calls(&cobra.Command{
		Use: "code", Short: "Show a one-use pairing code another computer joins with (ten minutes)",
		RunE: func(*cobra.Command, []string) error {
			// The code lives in the running engine, which an in-process core would stop at exit.
			c, err := a.daemonClient("a pairing code")
			if err != nil {
				return err
			}
			var code api.PairingCode
			if err := c.Do(context.Background(), "POST", "/v1/share/code", nil, &code); err != nil {
				return err
			}
			a.print(code, func() {
				a.printf("%s\n", code.Code)
				a.printf("Valid until %s, once.\n", code.ExpiresAt.Local().Format(time.TimeOnly))
			})
			return nil
		},
	}, "POST /v1/share/code"))
	return cmd
}
