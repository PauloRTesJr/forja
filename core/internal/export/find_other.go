//go:build !windows && !darwin

package export

import (
	"os"
	"path/filepath"
)

// installed lists where Blender may be on Linux beyond PATH: snap and flatpak.
func installed() []string {
	out := []string{"/snap/bin/blender", "/var/lib/flatpak/exports/bin/org.blender.Blender"}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".local", "share", "flatpak", "exports", "bin", "org.blender.Blender"))
	}
	return out
}
