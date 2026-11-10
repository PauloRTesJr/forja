// Package fakeblender stands in for Blender in tests, which must run where Blender is not
// installed. A test binary whose TestMain calls Main acts as Blender when started as one.
package fakeblender

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	envVersion = "FORJA_FAKE_BLENDER"
	envLog     = "FORJA_FAKE_BLENDER_LOG"
)

// Main acts as Blender and exits when this process was started as the fake; otherwise it
// returns at once. Call it first thing in TestMain.
func Main() {
	if version := os.Getenv(envVersion); version != "" {
		os.Exit(run(version, os.Args[1:]))
	}
}

// Use makes this test binary a fake Blender of the given version for the rest of t, and
// returns its path and the file where it records each asset it opens.
func Use(t testing.TB, version string) (path, opened string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = namedLikeBlender(t, exe)
	opened = filepath.Join(t.TempDir(), "opened.jsonl")
	t.Setenv(envVersion, version)
	t.Setenv(envLog, opened)
	return exe, opened
}

// Hangs is a version for which the fake never answers, as a program that is not Blender might not.
const Hangs = "hangs"

var (
	copyOnce sync.Once
	copyDir  string
	copyPath string
	copyErr  error
)

// namedLikeBlender is a copy of exe named as Forja requires of Blender's program. One copy serves
// the whole test process: a running program's file cannot be deleted on Windows, so a copy per
// test would break its temp folder's cleanup.
func namedLikeBlender(t testing.TB, exe string) string {
	t.Helper()
	copyOnce.Do(func() {
		copyDir, copyErr = os.MkdirTemp("", "fakeblender")
		if copyErr != nil {
			return
		}
		name := "blender"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		copyPath = filepath.Join(copyDir, name)
		var raw []byte
		if raw, copyErr = os.ReadFile(exe); copyErr == nil {
			copyErr = os.WriteFile(copyPath, raw, 0o755)
		}
	})
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	return copyPath
}

// Cleanup removes that copy; call it after m.Run in TestMain. Failing to is not a failure.
func Cleanup() {
	if copyDir != "" {
		_ = os.RemoveAll(copyDir)
	}
}

// Unreadable marks a GLB the fake cannot convert, as a broken file would fail in Blender.
const Unreadable = "fake-unreadable"

type job struct {
	Mode   string `json:"mode"`
	Format string `json:"format"`
	Items  []struct {
		GLB string `json:"glb"`
		Out string `json:"out"`
	} `json:"items"`
}

func run(version string, args []string) int {
	if version == Hangs {
		time.Sleep(time.Minute)
	}
	if slices.Contains(args, "--version") {
		fmt.Printf("Blender %s\n\tbuild date: 2026-01-01\n", version)
		return 0
	}
	i := slices.Index(args, "--")
	if i < 0 || i+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "fake blender: no job after --")
		return 2
	}
	raw := []byte(args[i+1])
	if !strings.HasPrefix(args[i+1], "{") {
		var err error
		if raw, err = os.ReadFile(args[i+1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	var j job
	if err := json.Unmarshal(raw, &j); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if j.Mode == "open" {
		f, err := os.OpenFile(os.Getenv(envLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return 2
		}
		defer f.Close()
		_, _ = f.Write(append(raw, '\n'))
		return 0
	}
	if !slices.Contains(args, "--background") || !slices.Contains(args, "--factory-startup") {
		fmt.Fprintln(os.Stderr, "fake blender: conversions must run in the background with factory settings")
		return 2
	}
	for n, it := range j.Items {
		glb, err := os.ReadFile(it.GLB)
		if err != nil || strings.Contains(string(glb), Unreadable) {
			report(map[string]any{"index": n, "error": fmt.Sprintf("RuntimeError: cannot read %s", it.GLB)})
			continue
		}
		files := []string{it.Out}
		_ = os.WriteFile(it.Out, []byte("fake "+j.Format+" of "+filepath.Base(it.GLB)), 0o644)
		if j.Format == "obj" {
			stem := strings.TrimSuffix(it.Out, filepath.Ext(it.Out))
			_ = os.WriteFile(stem+"_Image_0.png", []byte("texture"), 0o644)
			_ = os.WriteFile(stem+".mtl", []byte("map_Kd "+filepath.Base(stem)+"_Image_0.png\n"), 0o644)
			// Blender on Windows mixes separators in the paths it prints.
			sep := "/"
			if runtime.GOOS == "windows" {
				sep = `\`
			}
			files = append(files, filepath.ToSlash(filepath.Dir(it.Out))+sep+filepath.Base(stem)+"_Image_0.png", stem+".mtl")
		}
		report(map[string]any{"index": n, "files": files})
	}
	return 0
}

func report(v any) {
	raw, _ := json.Marshal(v)
	fmt.Println("Read blend: noise Blender prints")
	fmt.Println("FORJA " + string(raw))
}
