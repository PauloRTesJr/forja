//go:build darwin

package export

import (
	"os"
	"path/filepath"
)

// installed lists where Blender may be on macOS.
func installed() []string {
	out := []string{"/Applications/Blender.app/Contents/MacOS/Blender"}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, "Applications", "Blender.app", "Contents", "MacOS", "Blender"))
	}
	return out
}
