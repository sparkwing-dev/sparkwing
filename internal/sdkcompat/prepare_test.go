package sdkcompat

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	testleak.Main(m)
}

func TestPrepareReusesFrozenArtifactsAndRebuildsCold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake compiler uses a POSIX shell")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	must(t, os.MkdirAll(bin, 0o700))
	must(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
if [ "$1" = version ]; then
  printf 'go version go1.26.10 test/test\n'
  exit 0
fi
if [ "$GOWORK" != off ] || [ "$GOFLAGS" != -buildvcs=false ]; then
  exit 9
fi
printf 'compiled\n' >> "$COMPAT_COMPILER_CALLS"
printf '#!/bin/sh\nexit 0\n' > "$4"
`), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := filepath.Join(root, "calls")
	t.Setenv("COMPAT_COMPILER_CALLS", calls)
	fixture := Fixture{
		ID: "go-v0.1.0", Language: "go", Version: "v0.1.0", Directory: "fixture", Entrypoint: ".",
		Artifact: Artifact{Source: sdkModule, Checksum: "h1:frozen"},
	}
	source := filepath.Join(root, fixture.Directory)
	must(t, os.MkdirAll(source, 0o700))
	module := []byte("module example.com/frozen\n\ngo 1.26\n\nrequire " + sdkModule + " v0.1.0\n")
	must(t, os.WriteFile(filepath.Join(source, "go.mod"), module, 0o600))
	must(t, os.WriteFile(filepath.Join(source, "go.sum"), []byte(sdkModule+" v0.1.0 h1:frozen\n"), 0o600))
	options := Options{CacheDir: filepath.Join(root, "cache")}
	first, err := Prepare(context.Background(), root, fixture, options)
	must(t, err)
	if first.CacheHit {
		t.Fatal("first preparation reused an artifact")
	}
	warm, err := Prepare(context.Background(), root, fixture, options)
	must(t, err)
	if !warm.CacheHit {
		t.Fatal("warm preparation rebuilt the fixture")
	}
	if !slices.Equal(first.Command, warm.Command) {
		t.Fatal("warm preparation changed command")
	}
	options.Cold = true
	cold, err := Prepare(context.Background(), root, fixture, options)
	must(t, err)
	if cold.CacheHit {
		t.Fatal("cold preparation reused an artifact")
	}
	if first.Key != cold.Key {
		t.Fatal("cold preparation changed fixture identity")
	}
	if slices.Equal(first.Command, cold.Command) {
		t.Fatal("cold preparation returned the cached executable")
	}
	compiled, err := os.ReadFile(calls)
	must(t, err)
	if string(compiled) != "compiled\ncompiled\n" {
		t.Fatalf("compiler calls = %q", compiled)
	}
	unchanged, err := os.ReadFile(filepath.Join(source, "go.mod"))
	must(t, err)
	if !slices.Equal(module, unchanged) {
		t.Fatal("preparation changed frozen module")
	}
}

func TestReleasedGoFixtureRejectsReplacementAndWrongChecksum(t *testing.T) {
	fixture := Fixture{ID: "go-v0.1.0", Language: "go", Version: "v0.1.0", Artifact: Artifact{Source: sdkModule, Checksum: "h1:frozen"}}
	files := map[string][]byte{
		"go.mod": []byte("module example.com/frozen\nrequire " + sdkModule + " v0.1.0\nreplace " + sdkModule + " => ../..\n"),
		"go.sum": []byte(sdkModule + " v0.1.0 h1:frozen\n"),
	}
	mustContainError(t, validateArtifact(fixture, files), "forbids local module replacements")
	files["go.mod"] = []byte("module example.com/frozen\nrequire " + sdkModule + " v0.1.0\n")
	files["go.sum"] = []byte(sdkModule + " v0.1.0 h1:different\n")
	mustContainError(t, validateArtifact(fixture, files), "does not pin its released SDK")
}

func TestPublishedNpmFixtureRejectsChangedIntegrity(t *testing.T) {
	fixture := Fixture{ID: "typescript-0.1.0", Language: "typescript", Version: "0.1.0", Artifact: Artifact{Source: "https://registry.npmjs.org/@sparkwing/sdk/-/sdk-0.1.0.tgz", Checksum: "sha512-frozen"}}
	files := map[string][]byte{
		"package-lock.json": []byte(`{"packages":{"node_modules/@sparkwing/sdk":{"version":"0.1.0","resolved":"https://registry.npmjs.org/@sparkwing/sdk/-/sdk-0.1.0.tgz","integrity":"sha512-different"}}}`),
	}
	mustContainError(t, validateArtifact(fixture, files), "does not pin its published npm integrity")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustContainError(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("error = %v, want %q", err, fragment)
	}
}
