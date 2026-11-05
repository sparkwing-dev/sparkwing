package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

func TestRunnerServiceRetainsEnrolledGoToolchain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Unix service registration")
	}
	root := t.TempDir()
	goRoot := filepath.Join(root, "asdf Go & $tools % cache")
	shimDir := filepath.Join(root, "shims")
	for _, dir := range []string{filepath.Join(goRoot, "bin"), shimDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	goBinary := filepath.Join(goRoot, "bin", "go")
	if err := os.WriteFile(goBinary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	shim := fmt.Sprintf("#!/bin/sh\n[ \"$GOTOOLCHAIN\" = local ] || exit 1\n[ \"$1 $2\" = 'env GOROOT' ] || exit 1\nprintf '%%s\\n' '%s'\n", strings.ReplaceAll(goRoot, "'", "'\\''"))
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir)
	t.Setenv("HOME", root)
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "state"))
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			host, err := defaultRunnerServiceHost(filepath.Join(root, "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			host.GOOS, host.Binary = platform, filepath.Join(root, "sparkwing-runner")
			host.Exec = func(_ context.Context, name string, args ...string) (string, error) {
				if name == "launchctl" && args[0] == "print" {
					return "could not find service", errors.New("exit status 1")
				}
				return "", nil
			}
			state, err := runnerservice.Install(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(state.Path)
			if err != nil {
				t.Fatal(err)
			}
			path := installedRunnerPath(t, platform, string(body))
			got, err := gotoolchain.Executable([]string{"PATH=" + path})
			if err != nil || got != goBinary {
				t.Fatalf("service Go = %q, %v; want enrolled toolchain %q (PATH=%q)", got, err, goBinary, path)
			}
			if !strings.Contains(path, shimDir) {
				t.Fatalf("service PATH lost enrollment tools: %q", path)
			}
		})
	}
}

func installedRunnerPath(t *testing.T, platform, body string) string {
	t.Helper()
	if platform == "linux" {
		for line := range strings.SplitSeq(body, "\n") {
			if value, ok := strings.CutPrefix(line, "Environment="); ok {
				assignment, err := strconv.Unquote(value)
				if err == nil && strings.HasPrefix(assignment, "PATH=") {
					return strings.ReplaceAll(strings.TrimPrefix(assignment, "PATH="), "%%", "%")
				}
			}
		}
	} else {
		decoder := xml.NewDecoder(strings.NewReader(body))
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			if start, ok := token.(xml.StartElement); ok && start.Name.Local == "key" {
				var key string
				if err := decoder.DecodeElement(&key, &start); err != nil {
					t.Fatal(err)
				}
				if key == "PATH" {
					var path string
					if err := decoder.DecodeElement(&path, nil); err != nil {
						t.Fatal(err)
					}
					return path
				}
			}
		}
	}
	t.Fatal("runner registration has no PATH")
	return ""
}
