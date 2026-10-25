package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
)

// Vendors `forja providers add` and `test` take before the address, in the app's words.
var vendors = map[string]string{
	"runpod":     "RunPod serverless: the endpoint id; its API key on stdin",
	"runpod-pod": "a RunPod pod: its address; its token on stdin",
	"modal":      "a Modal gateway: its address; its token on stdin",
	"comfyui":    "ComfyUI: its address and --workflow <stage>=<file>; with --token-stdin, an https address",
}

// cloudFlags are the settings of providers that are not another computer.
type cloudFlags struct {
	label, volume, datacenter, volumeKeyID string
	price, pricePerHour                    float64
	workflows                              []string
	tokenStdin                             bool
}

func (f *cloudFlags) register(cmd *cobra.Command) {
	cmd.Flags().Float64Var(&f.price, "price", 0, "what its GPU costs, in USD per second (RunPod, Modal, a paid ComfyUI)")
	cmd.Flags().Float64Var(&f.pricePerHour, "price-per-hour", 0, "what a RunPod pod costs, in USD per hour")
	cmd.Flags().StringVar(&f.volume, "volume", "", "RunPod: the endpoint's network volume, to move files through it by hash")
	cmd.Flags().StringVar(&f.datacenter, "datacenter", "", "RunPod: the volume's datacenter, such as EU-RO-1")
	cmd.Flags().StringVar(&f.volumeKeyID, "volume-key-id", "", "RunPod: the volume's S3 access key; its secret is the next line on stdin")
	cmd.Flags().StringArrayVar(&f.workflows, "workflow", nil, "ComfyUI: <stage type>=<API-format workflow file>, repeatable")
}

// readSecrets reads one secret per line: typed at a terminal, or from stdin in order.
func (a *app) readSecrets(whats ...string) ([]string, error) {
	if a.tty || len(whats) == 1 {
		out := make([]string, len(whats))
		for i, w := range whats {
			s, err := a.readSecret(w)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	raw, err := io.ReadAll(io.LimitReader(a.in, 64<<10))
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(withoutBOM(string(raw)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < len(whats) {
		return nil, fmt.Errorf("give the %s on stdin, one per line", strings.Join(whats, " and the "))
	}
	return lines[:len(whats)], nil
}

// cloudBody is the request for a vendor's provider, as POST /v1/providers and its test take it.
func (a *app) cloudBody(vendor, target string, f *cloudFlags) (map[string]any, error) {
	body := map[string]any{"label": f.label}
	switch vendor {
	case "runpod":
		body["kind"], body["vendor"], body["endpoint_id"], body["price_usd_per_second"] = "serverless", "runpod", target, f.price
		whats := []string{"RunPod API key"}
		if f.volume != "" {
			body["volume"], body["region"], body["volume_key_id"] = f.volume, f.datacenter, f.volumeKeyID
			whats = append(whats, "volume's S3 secret")
		}
		keys, err := a.readSecrets(whats...)
		if err != nil {
			return nil, err
		}
		body["token"] = keys[0]
		if len(keys) > 1 {
			body["volume_secret"] = keys[1]
		}
	case "runpod-pod":
		body["kind"], body["vendor"], body["url"], body["price_usd_per_hour"] = "engine", "runpod", target, f.pricePerHour
		token, err := a.readSecret("pod's access token")
		if err != nil {
			return nil, err
		}
		body["token"] = token
	case "modal":
		body["kind"], body["vendor"], body["url"], body["price_usd_per_second"] = "serverless", "modal", target, f.price
		token, err := a.readSecret("gateway's access token")
		if err != nil {
			return nil, err
		}
		body["token"] = token
	case "comfyui":
		body["kind"], body["url"], body["price_usd_per_second"] = "comfyui", target, f.price
		workflows := map[string]string{}
		for _, w := range f.workflows {
			use, file, ok := strings.Cut(w, "=")
			if !ok || use == "" || file == "" {
				return nil, fmt.Errorf("--workflow %q: give it as <stage type>=<file>, such as shape=hunyuan.json", w)
			}
			abs, err := filepath.Abs(file)
			if err != nil {
				return nil, err
			}
			workflows[use] = abs
		}
		body["workflows"] = workflows
		if f.tokenStdin {
			token, err := a.readSecret("access token")
			if err != nil {
				return nil, err
			}
			body["token"] = token
		}
	default:
		return nil, fmt.Errorf("unknown provider %q: use runpod, runpod-pod, modal or comfyui, or give only an address", vendor)
	}
	return body, nil
}

func (a *app) showCloudProbe(p api.ProbeView) {
	a.printf("  %s  %s, %d ms", p.URL, p.Version, p.Latency)
	if p.GPU != "" {
		a.printf(", %s", p.GPU)
	}
	a.printf("\n")
	if p.Detail != "" {
		a.printf("  %s\n", p.Detail)
	}
	if p.Fingerprint != "" {
		a.printf("  certificate %s\n", p.Fingerprint)
	}
}

func vendorHelp() string {
	return "  runpod <endpoint-id>      " + vendors["runpod"] + "\n" +
		"  runpod-pod <address>      " + vendors["runpod-pod"] + "\n" +
		"  modal <url>               " + vendors["modal"] + "\n" +
		"  comfyui <address>         " + vendors["comfyui"] + "\n"
}
