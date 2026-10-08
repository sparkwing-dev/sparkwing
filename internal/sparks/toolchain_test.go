package sparks_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
)

func resolveFloorFixture(t *testing.T, setting, floor, listBody, downloadBody string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"printf '%s|%s\\n' \"$1\" \"$GOTOOLCHAIN\" >> '" + log + "'\n" +
		"case \"$1\" in\n" +
		"env) printf '%s\\n' '" + setting + "' 'go1.26.6' 'off'; exit 0;;\n" +
		"list) " + listBody + ";;\n" +
		"mod) " + downloadBody + ";;\nesac\n"
	for path, data := range map[string]string{"go": script, "go.mod": "module example.com/pipeline\ngo " + floor + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SPARKS_GO_BIN", filepath.Join(dir, "go"))
	t.Setenv("GOTOOLCHAIN", setting)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPRIVATE", "example.com/private")
	t.Setenv("GOPROXY", "https://proxy.invalid")
	return dir, log
}

func privateManifest() *sparks.Manifest {
	return &sparks.Manifest{Libraries: []sparks.Library{{Name: "private", Source: "example.com/private", Version: "latest"}}}
}

func TestResolveAndWriteUsesFloorForListAndDownload(t *testing.T) {
	dir, log := resolveFloorFixture(t, "go1.26.6", "1.26.8", "printf 'go: downloading toolchain\\n' >&2; printf '{\"Version\":\"v1.2.3\"}\\n'; exit 0", "exit 0")
	var notices []string
	ctx := gotoolchain.WithSession(context.Background(), os.Environ(), func(s string) { notices = append(notices, s) })
	changed, err := sparks.ResolveAndWrite(ctx, dir, privateManifest())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("overlay was not written")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"list|go1.26.8", "mod|go1.26.8"} {
		if !strings.Contains(string(data), call) {
			t.Errorf("missing %q in calls: %s", call, data)
		}
	}
	if strings.Count(string(data), "env|") != 1 || len(notices) != 1 {
		t.Fatalf("calls %s, notices %v", data, notices)
	}
	if os.Getenv("GOTOOLCHAIN") != "go1.26.6" {
		t.Fatal("changed process runtime pin")
	}
	if _, err := os.Stat(filepath.Join(dir, sparks.OverlaySumfileName)); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAndWriteExplainsLocalDependencyFloor(t *testing.T) {
	for _, failAt := range []string{"list", "download"} {
		t.Run(failAt, func(t *testing.T) {
			failure := "printf 'go: example.com/dependency requires go >= 1.27.2 (running go 1.26.6; GOTOOLCHAIN=local)\\n' >&2; exit 1"
			listBody, downloadBody := "printf '{\"Version\":\"v1.2.3\"}\\n'; exit 0", "exit 0"
			if failAt == "list" {
				listBody = failure
			} else {
				downloadBody = failure
			}
			dir, _ := resolveFloorFixture(t, "local", "1.26.0", listBody, downloadBody)
			_, err := sparks.ResolveAndWrite(context.Background(), dir, privateManifest())
			var floorErr *gotoolchain.Error
			if !errors.As(err, &floorErr) {
				t.Fatalf("resolve error = %v", err)
			}
			for _, fragment := range []string{"1.27.2", "1.26.6", "GOTOOLCHAIN=local", "the GOTOOLCHAIN environment variable", "unset GOTOOLCHAIN"} {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("missing %q in error: %v", fragment, err)
				}
			}
		})
	}
}

func TestResolveAndWriteIgnoresTheFloorOfTheOverlayItReplaces(t *testing.T) {
	dir, _ := resolveFloorFixture(t, "local", "1.26.0", "printf '{\"Version\":\"v1.2.3\"}\\n'; exit 0", "exit 0")
	stale := "module example.com/pipeline\ngo 1.27.0\n"
	if err := os.WriteFile(filepath.Join(dir, sparks.OverlayModfileName), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := gotoolchain.WithSession(context.Background(), os.Environ(), func(string) {})
	if _, err := sparks.ResolveAndWrite(ctx, dir, privateManifest()); err != nil {
		t.Fatalf("a stale overlay blocked its own regeneration: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, sparks.OverlayModfileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "go 1.27.0") {
		t.Fatalf("overlay kept the stale floor:\n%s", data)
	}
}

func TestResolverWithDirUsesTheModuleFloor(t *testing.T) {
	dir, log := resolveFloorFixture(t, "go1.26.6", "1.26.8", "printf '{\"Version\":\"v1.2.3\"}\\n'; exit 0", "exit 0")
	resolver := sparks.NewResolverFromEnv()
	resolver.Dir = dir
	ctx := gotoolchain.WithSession(context.Background(), os.Environ(), func(string) {})
	if _, err := resolver.Resolve(ctx, privateManifest()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "list|go1.26.8") {
		t.Fatalf("go list ran without the floor override: %s", data)
	}
}
