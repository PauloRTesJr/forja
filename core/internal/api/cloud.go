package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"forja/internal/config"
	"forja/internal/paths"
	"forja/internal/provider"
	"forja/internal/provider/comfyui"
	"forja/internal/provider/engine"
	"forja/internal/provider/fal"
	"forja/internal/provider/runpod"
	"forja/internal/providers"
	"forja/internal/secrets"
)

// cloudBody is how the app and the CLI add a GPU they rent (RunPod, Modal) or a ComfyUI.
type cloudBody struct {
	Kind         string  `json:"kind"`
	Vendor       string  `json:"vendor"`
	Label        string  `json:"label"`
	URL          string  `json:"url"`
	Token        string  `json:"token"`
	EndpointID   string  `json:"endpoint_id"`
	Price        float64 `json:"price_usd_per_second"`
	Volume       string  `json:"volume"`
	Region       string  `json:"region"`
	VolumeKeyID  string  `json:"volume_key_id"`
	VolumeSecret string  `json:"volume_secret"`
	// Workflows maps stage types to workflow files on this computer; the core keeps copies.
	Workflows map[string]string `json:"workflows"`
}

func cloudKind(kind string) bool {
	return kind == string(provider.KindServerless) || kind == string(provider.KindComfyUI)
}

// cloud is a provider to add, checked: its config, the secrets it needs, what the check found.
type cloud struct {
	pc        config.Provider
	key       string
	volumeKey string
	probe     ProbeView
	// workflows are the checked files to copy into the config folder, by stage type.
	workflows map[string][]byte
}

// checkCloud validates a body and tries what it describes, before anything is saved.
func (s *Server) checkCloud(ctx context.Context, b cloudBody) (*cloud, error) {
	if b.Price < 0 {
		return nil, errors.New("a price cannot be negative")
	}
	c := &cloud{key: strings.TrimSpace(b.Token)}
	c.pc = config.Provider{Kind: b.Kind, Vendor: b.Vendor, Label: strings.TrimSpace(b.Label), PriceUSDPerSecond: b.Price}
	start := time.Now()
	switch {
	case b.Kind == string(provider.KindServerless) && b.Vendor == runpod.Vendor:
		c.pc.EndpointID, c.pc.Paid = strings.TrimSpace(b.EndpointID), true
		if c.pc.EndpointID == "" || strings.ContainsAny(c.pc.EndpointID, "/?# ") {
			return nil, errors.New("enter the endpoint ID shown on the endpoint's page in the RunPod console")
		}
		if c.key == "" {
			return nil, errors.New("paste a RunPod API key")
		}
		var vol *runpod.Volume
		if v := strings.TrimSpace(b.Volume); v != "" {
			region, id, secret := strings.ToUpper(strings.TrimSpace(b.Region)), strings.TrimSpace(b.VolumeKeyID), strings.TrimSpace(b.VolumeSecret)
			if region == "" || id == "" || secret == "" {
				return nil, errors.New("a network volume needs its datacenter and an S3 API key (access key and secret)")
			}
			if !runpod.ValidDatacenter(region) {
				return nil, fmt.Errorf("%q is not a RunPod datacenter: give the volume's, such as EU-RO-1", b.Region)
			}
			vol = &runpod.Volume{ID: v, Datacenter: region, AccessKey: id, Secret: secret}
			c.pc.Volume, c.pc.Region, c.volumeKey = v, region, id+":"+secret
		}
		p := runpod.New("", c.pc.EndpointID, c.key, b.Price, vol, s.Store)
		workers, err := p.Check(ctx)
		if err != nil {
			return nil, err
		}
		c.probe = ProbeView{URL: p.URL(), Version: "RunPod serverless",
			Detail: fmt.Sprintf("%d workers idle, %d running, %d jobs queued", workers.Idle, workers.Running, workers.Queued)}
		if c.pc.Label == "" {
			c.pc.Label = "RunPod " + c.pc.EndpointID
		}
	case b.Kind == string(provider.KindServerless) && b.Vendor == providers.Modal:
		u, err := gatewayURL(b.URL)
		if err != nil {
			return nil, err
		}
		if c.key == "" {
			return nil, errors.New("paste the access token from the app's Modal secret")
		}
		if err := checkGateway(ctx, u, c.key); err != nil {
			return nil, err
		}
		c.pc.URL, c.pc.Paid = u, true
		c.probe = ProbeView{URL: u, Version: "Modal gateway", Detail: "a GPU starts when a stage runs"}
		if c.pc.Label == "" {
			c.pc.Label = "Modal"
		}
	case b.Kind == string(provider.KindComfyUI):
		u, err := comfyURL(b.URL, c.key != "")
		if err != nil {
			return nil, err
		}
		if len(b.Workflows) == 0 {
			return nil, errors.New("give at least one workflow, for background removal, shape or texture")
		}
		c.workflows = map[string][]byte{}
		for use, path := range b.Workflows {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("the %s workflow: %w", use, err)
			}
			if _, err := comfyui.Parse(use, filepath.Base(path), raw); err != nil {
				return nil, err
			}
			c.workflows[use] = raw
		}
		stats, err := comfyui.New("", u, c.key, nil, false, 0, s.Store).Stats(ctx)
		if err != nil {
			return nil, err
		}
		c.pc.URL, c.pc.Paid = u, b.Price > 0
		c.probe = ProbeView{URL: u, Version: "ComfyUI " + stats.System.Version}
		if len(stats.Devices) > 0 {
			c.probe.GPU = stats.Devices[0].Name
		}
		if c.pc.Label == "" {
			c.pc.Label = "ComfyUI"
		}
	default:
		return nil, fmt.Errorf("Forja cannot add a %s provider from %q", b.Kind, b.Vendor)
	}
	c.probe.Latency = time.Since(start).Milliseconds()
	return c, nil
}

// gatewayURL accepts a Modal gateway's https address (or plain http on this machine, for a
// gateway tried locally).
func gatewayURL(addr string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(addr))
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return "", fmt.Errorf("%q is not a gateway address like https://you--forja-engine-gateway.modal.run", addr)
	}
	return u.Scheme + "://" + u.Host, nil
}

// comfyURL turns "192.168.1.30" into ComfyUI's usual http://192.168.1.30:8188. A ComfyUI that
// takes a token needs https, unless it is on this computer: plain http would show the token
// to anyone on the way.
func comfyURL(addr string, withToken bool) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("enter ComfyUI's address")
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%q is not an address like 192.168.1.30:8188", addr)
	}
	if withToken && u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return "", fmt.Errorf("Forja sends an access token only over https: give the proxy's https address, "+
			"such as https://%s, or leave the token empty", u.Hostname())
	}
	if u.Port() == "" && u.Scheme == "http" {
		u.Host = net.JoinHostPort(u.Hostname(), "8188")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// checkGateway proves a gateway answers and takes the token, without waking a GPU: an
// artifact check needs the token and nothing else.
func checkGateway(ctx context.Context, base, token string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, base+"/v1/artifacts/blake3:"+strings.Repeat("0", 64), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", base, err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New("the gateway refused the token")
	}
	return fmt.Errorf("the gateway answered %s", resp.Status)
}

// cloudTest tries a provider's settings before they are saved.
func (s *Server) cloudTest(w http.ResponseWriter, r *http.Request, raw []byte) {
	var b cloudBody
	if err := json.Unmarshal(raw, &b); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, err := s.checkCloud(ctx, b)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, c.probe)
}

// cloudAdd adds a RunPod endpoint, a Modal gateway or a ComfyUI: checked, its keys in the
// keychain, its workflows copied, then after the providers in routing.priority.
func (s *Server) cloudAdd(w http.ResponseWriter, r *http.Request, raw []byte) {
	var b cloudBody
	if err := json.Unmarshal(raw, &b); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, err := s.checkCloud(ctx, b)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	pc := c.pc
	pc.ID = s.newProviderID(pc.Label)
	if c.key != "" {
		pc.Secret = providers.SecretName(pc.ID)
		if err := secrets.Set(pc.Secret, c.key); err != nil {
			problem(w, 500, "could not store the key in "+secrets.Store()+": "+err.Error())
			return
		}
	}
	if c.volumeKey != "" {
		if err := secrets.Set(providers.VolumeSecretName(pc.ID), c.volumeKey); err != nil {
			s.forgetSecrets(pc)
			problem(w, 500, "could not store the S3 key in "+secrets.Store()+": "+err.Error())
			return
		}
	}
	if len(c.workflows) > 0 {
		pc.Workflows = map[string]string{}
		dir := filepath.Join(paths.ConfigDir(), "comfyui", pc.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			problem(w, 500, err.Error())
			return
		}
		for use, raw := range c.workflows {
			if err := os.WriteFile(filepath.Join(dir, use+".json"), raw, 0o644); err != nil {
				problem(w, 500, err.Error())
				return
			}
			pc.Workflows[use] = "comfyui/" + pc.ID + "/" + use + ".json"
		}
	}
	if err := config.AddProvider(config.UserFile(), pc, validateUser); err != nil {
		s.forgetSecrets(pc)
		problem(w, 422, err.Error())
		return
	}
	if cfg, err := config.Load("", nil); err == nil && !slices.Contains(cfg.Routing.Priority, pc.ID) {
		priority := append(slices.Clone(cfg.Routing.Priority), pc.ID)
		_ = config.Edit(config.UserFile(), map[string]any{"routing.priority": anySlice(priority)}, validateUser)
	}
	s.rebuild(pc)
	probe := c.probe
	writeJSON(w, 201, AddedProvider{ID: pc.ID, Label: pc.Label, Probe: &probe})
}

// forgetSecrets removes a provider's keys and the workflows the core kept for it.
func (s *Server) forgetSecrets(pc config.Provider) {
	if pc.Secret != "" {
		_ = secrets.Delete(pc.Secret)
	}
	if pc.Volume != "" {
		_ = secrets.Delete(providers.VolumeSecretName(pc.ID))
	}
	if len(pc.Workflows) > 0 {
		_ = os.RemoveAll(filepath.Join(paths.ConfigDir(), "comfyui", pc.ID))
	}
}

// testKey tries a provider's new key or token the way that provider checks one.
func (s *Server) testKey(ctx context.Context, pc config.Provider, key string) error {
	switch {
	case pc.Kind == string(provider.KindEngine):
		_, err := engine.Test(ctx, pc.URL, key, pc.Fingerprint)
		return err
	case pc.Kind == string(provider.KindServerless) && pc.Vendor == providers.Modal:
		return checkGateway(ctx, pc.URL, key)
	case pc.Kind == string(provider.KindAPI) && pc.Vendor == fal.Vendor:
		return fal.New(pc.ID, key, pc.Models, s.Store).Health(ctx)
	}
	p, err := providers.BuildWithKey(pc, s.Store, key)
	if err != nil {
		return err
	}
	return p.Health(ctx)
}
