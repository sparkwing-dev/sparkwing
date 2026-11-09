package bincache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestGoEnvFIPSChangesCacheKey(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-buildvcs=false -p=1")
	t.Setenv("GOFIPS140", "")
	config := filepath.Join(t.TempDir(), "goenv")
	t.Setenv("GOENV", config)
	dir := newPipelineDir(t)
	writeFile(t, config, "GOFIPS140=off\n")
	first := mustKey(t, dir)
	writeFile(t, config, "GOFIPS140=latest\n")
	if mustKey(t, dir) == first {
		t.Fatal("FIPS build reused the non-FIPS cache identity")
	}
}

func TestGoFileOverlayChangesRebuildPipeline(t *testing.T) {
	for _, added := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "overlay-only"}[added], func(t *testing.T) {
			t.Setenv("GOWORK", "off")
			t.Setenv("SPARKWING_HOME", t.TempDir())
			dir := newPipelineDir(t)
			original := filepath.Join(dir, "value.go")
			if !added {
				writeFile(t, original, "package main\nconst value = \"original\"\n")
			}
			writeFile(t, filepath.Join(dir, "main.go"), "package main\nfunc main(){print(value)}\n")
			replacement := filepath.Join(t.TempDir(), "replacement.go")
			manifest := filepath.Join(t.TempDir(), "file overlay.json")
			body, err := json.Marshal(map[string]any{"Replace": map[string]string{original: replacement}})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, manifest, string(body))
			t.Setenv("GOFLAGS", "-buildvcs=false -p=1 '-overlay="+manifest+"'")
			for _, value := range []string{"first", "second"} {
				writeFile(t, replacement, "package main\nconst value = \""+value+"\"\n")
				if out, built := materializeFixture(t, dir); out != value || !built {
					t.Fatalf("overlay build = %q, %t; want %s and rebuild", out, built, value)
				}
			}
			if out, built := materializeFixture(t, dir); out != "second" || built {
				t.Fatalf("unchanged overlay = %q, %t; want cache hit", out, built)
			}
		})
	}
}

func TestGoPGOChangesRebuildPipeline(t *testing.T) {
	root, err := procenv.ControlCommandContext(t.Context(), os.Environ(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	profiles := []string{
		filepath.Join(strings.TrimSpace(string(root)), "src", "cmd", "compile", "default.pgo"),
		filepath.Join(strings.TrimSpace(string(root)), "src", "cmd", "compile", "internal", "test", "testdata", "pgo", "inline", "inline_hot.pprof"),
	}
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "ignored-default", true: "external"}[external], func(t *testing.T) {
			t.Setenv("GOWORK", "off")
			t.Setenv("SPARKWING_HOME", t.TempDir())
			dir := newGitPipelineDir(t)
			writeFile(t, filepath.Join(dir, ".gitignore"), "default.pgo\n")
			profile := filepath.Join(dir, "default.pgo")
			flags := "-buildvcs=false -p=1"
			if external {
				profile = filepath.Join(t.TempDir(), "external profile.pprof")
				flags += " '--pgo=" + profile + "'"
			}
			t.Setenv("GOFLAGS", flags)
			for _, source := range profiles {
				data, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, profile, string(data))
				if _, built := materializeFixture(t, dir); !built {
					t.Fatal("changed PGO profile reused the previous binary")
				}
			}
			if _, built := materializeFixture(t, dir); built {
				t.Fatal("unchanged PGO profile rebuilt")
			}
		})
	}
}

func TestSymlinkedHomeKeepsOwnerCacheLimits(t *testing.T) {
	owner := t.TempDir()
	alias := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(owner, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("SPARKWING_HOME", alias)
	writeCacheConfig(t, "cache:\n  max_bytes: 0\n  max_entries: 1\n")
	for _, key := range []string{"00000000-00000001", "00000000-00000002", "00000000-00000003"} {
		entry, err := PipelineEntry(key)
		if err != nil {
			t.Fatal(err)
		}
		lease, _, err := entry.AcquireOrMaterialize(t.Context(), func(path string) error {
			return os.WriteFile(path, []byte("binary"), 0o700)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := PipelineEntry("00000000-00000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("home alias disabled owner pruning: %v", err)
	}
}
