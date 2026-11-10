//go:build windows

package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnWindowsBlenderIsItsExeAndNotAScriptThatStartsIt(t *testing.T) {
	// cmd.exe would split the script Blender runs at its line breaks.
	shim := filepath.Join(t.TempDir(), "blender.cmd")
	if err := os.WriteFile(shim, []byte(`@"C:\Blender\blender.exe" %*`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FindBlender(shim); err == nil || !strings.Contains(err.Error(), "is not Blender's program") {
		t.Fatalf("a .cmd shim: %v", err)
	}
	for cmd, want := range map[string]string{
		`"C:\Program Files\Blender Foundation\Blender 5.2\blender-launcher.exe" "%1"`: `C:\Program Files\Blender Foundation\Blender 5.2\blender-launcher.exe`,
		`D:\Blender\blender.exe "%1"`: `D:\Blender\blender.exe`,
	} {
		if got := firstArg(cmd); got != want {
			t.Errorf("firstArg(%s) = %s, want %s", cmd, got, want)
		}
	}
}
