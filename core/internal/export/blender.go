package export

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

//go:embed blender.py
var blenderScript string

// MinBlender is the oldest Blender the conversions run on: 4.2 LTS has every importer and
// exporter they call.
var MinBlender = [2]int{4, 2}

// Blender is a Blender installation the core runs to convert and open assets.
type Blender struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// ErrNoBlender says that no Blender was found on PATH or where its installers put it.
var ErrNoBlender = fmt.Errorf("Blender was not found: install Blender %d.%d or newer, or set its path in Settings",
	MinBlender[0], MinBlender[1])

var errTooOld = errors.New("too old")

// FindBlender returns the Blender at configured when it is set, else the first one new
// enough on PATH or where its installers put it.
func FindBlender(configured string) (*Blender, error) {
	if configured != "" {
		return probe(configured)
	}
	var old error
	for _, path := range candidates() {
		b, err := probe(path)
		if err == nil {
			return b, nil
		}
		if old == nil && errors.Is(err, errTooOld) {
			old = err
		}
	}
	if old != nil {
		return nil, old
	}
	return nil, ErrNoBlender
}

func candidates() []string {
	var out []string
	if p, err := exec.LookPath(onPath); err == nil {
		out = append(out, p)
	}
	return append(out, installed()...)
}

var versionInName = regexp.MustCompile(`(\d+)\.(\d+)`)

// newestFirst orders Blender installs by the version in their folder names, such as
// "Blender 4.10" after "Blender 5.2" and before "Blender 4.2".
func newestFirst(paths []string) []string {
	key := func(p string) [2]int {
		m := versionInName.FindStringSubmatch(filepath.Base(filepath.Dir(p)))
		if m == nil {
			return [2]int{}
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		return [2]int{major, minor}
	}
	slices.SortStableFunc(paths, func(a, b string) int {
		ka, kb := key(a), key(b)
		if ka[0] != kb[0] {
			return kb[0] - ka[0]
		}
		return kb[1] - ka[1]
	})
	return paths
}

type probed struct {
	size    int64
	mod     time.Time
	version string
	err     error
}

var (
	probeMu sync.Mutex
	probes  = map[string]probed{}
)

// probe asks the program at path for its version, once per change of the file: starting
// Blender to ask takes a moment, and the app asks whenever it shows the export formats.
func probe(path string) (*Blender, error) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return nil, fmt.Errorf("there is no Blender at %s", path)
	}
	if err := runnable(path); err != nil {
		return nil, err
	}
	probeMu.Lock()
	p, ok := probes[path]
	probeMu.Unlock()
	if !ok || p.size != st.Size() || !p.mod.Equal(st.ModTime()) {
		p = probed{size: st.Size(), mod: st.ModTime()}
		p.version, p.err = askVersion(path)
		probeMu.Lock()
		probes[path] = p
		probeMu.Unlock()
	}
	if p.err != nil {
		return nil, p.err
	}
	return &Blender{Path: path, Version: p.version}, nil
}

var versionLine = regexp.MustCompile(`Blender (\d+)\.(\d+)(?:\.(\d+))?`)

func askVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	hide(cmd)
	out, err := cmd.Output()
	m := versionLine.FindSubmatch(out)
	if m == nil {
		if err != nil {
			return "", fmt.Errorf("%s did not answer as Blender: %w", path, err)
		}
		return "", fmt.Errorf("%s did not answer as Blender", path)
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	version := string(m[1]) + "." + string(m[2])
	if len(m[3]) > 0 {
		version += "." + string(m[3])
	}
	if major < MinBlender[0] || major == MinBlender[0] && minor < MinBlender[1] {
		return "", fmt.Errorf("Blender %s at %s is %w: exports need %d.%d or newer", version, path, errTooOld,
			MinBlender[0], MinBlender[1])
	}
	return version, nil
}

// job is what blender.py reads after "--": a JSON file, or the JSON itself.
type job struct {
	Mode   string    `json:"mode"`
	Format string    `json:"format,omitempty"`
	Items  []jobItem `json:"items"`
}

type jobItem struct {
	GLB string `json:"glb"`
	Out string `json:"out,omitempty"`
}

// converted is one line blender.py prints per item.
type converted struct {
	Index int      `json:"index"`
	Files []string `json:"files"`
	Error string   `json:"error"`
}

// convert runs Blender in the background over items and says what became of each. It runs
// with factory settings, so the user's add-ons and startup file cannot change the files.
func (b *Blender) convert(ctx context.Context, format string, items []jobItem, jobFile string) ([]converted, error) {
	raw, _ := json.Marshal(job{Mode: "export", Format: format, Items: items})
	if err := os.WriteFile(jobFile, raw, 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute+time.Duration(len(items))*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.Path, "--background", "--factory-startup", "--python-exit-code", "1",
		"--python-expr", blenderScript, "--", jobFile)
	hide(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Blender did not finish: %w", context.Cause(ctx))
	}
	results := make([]converted, len(items))
	got := 0
	sc := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "FORJA ")
		if !ok {
			continue
		}
		var c converted
		if json.Unmarshal([]byte(line), &c) == nil && c.Index >= 0 && c.Index < len(results) {
			results[c.Index] = c
			got++
		}
	}
	if got == 0 {
		why := lastLines(out.String(), 6)
		if runErr != nil {
			why = runErr.Error() + ": " + why
		}
		return nil, fmt.Errorf("Blender %s exported nothing: %s", b.Version, why)
	}
	for i := range results {
		if results[i].Files == nil && results[i].Error == "" {
			results[i].Error = "Blender stopped before it got to this asset: " + lastLines(out.String(), 3)
		}
	}
	return results, nil
}

func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines[max(0, len(lines)-n):], " / ")
}

// Open starts Blender, as the user set it up, with the GLBs imported into a new, unsaved
// file, and returns once it has started: the user carries on in it.
func (b *Blender) Open(glbs []string) error {
	items := make([]jobItem, len(glbs))
	for i, g := range glbs {
		items[i].GLB = g
	}
	raw, _ := json.Marshal(job{Mode: "open", Items: items})
	cmd := exec.Command(guiBinary(b.Path), "--python-expr", blenderScript, "--", asciiJSON(raw))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting Blender: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// asciiJSON escapes every non-ASCII character of a JSON document, so it survives any
// command-line encoding on its way to Blender.
func asciiJSON(raw []byte) string {
	var b strings.Builder
	for _, r := range string(raw) {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&b, `\u%04x`, u)
		}
	}
	return b.String()
}
