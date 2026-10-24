//go:build windows

package export

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// installed lists where Blender may be on Windows: the program that opens .blend files, then
// the installer's folders, newest version first, then Steam's.
func installed() []string {
	var out []string
	if k, err := registry.OpenKey(registry.CLASSES_ROOT, `blendfile\shell\open\command`, registry.QUERY_VALUE); err == nil {
		if cmd, _, err := k.GetStringValue(""); err == nil {
			if exe := firstArg(cmd); exe != "" {
				// It names blender-launcher.exe, which leaves before Blender finishes.
				out = append(out, filepath.Join(filepath.Dir(exe), "blender.exe"))
			}
		}
		k.Close()
	}
	for _, env := range []string{"ProgramW6432", "ProgramFiles"} {
		if dir := os.Getenv(env); dir != "" {
			found, _ := filepath.Glob(filepath.Join(dir, "Blender Foundation", "Blender*", "blender.exe"))
			out = append(out, newestFirst(found)...)
		}
	}
	if dir := os.Getenv("ProgramFiles(x86)"); dir != "" {
		out = append(out, filepath.Join(dir, "Steam", "steamapps", "common", "Blender", "blender.exe"))
	}
	return out
}

// firstArg is the program a registry command line runs.
func firstArg(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if rest, ok := strings.CutPrefix(cmd, `"`); ok {
		exe, _, _ := strings.Cut(rest, `"`)
		return exe
	}
	exe, _, _ := strings.Cut(cmd, " ")
	return exe
}
