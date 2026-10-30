package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/pkg/scaffold"
)

func TestScaffoldPinMatchesBuildKind(t *testing.T) {
	previous := Version
	t.Cleanup(func() { Version = previous })
	for _, version := range []string{"v0.67.0", "(devel)", "v0.68.0-qa.16"} {
		Version = version
		want := version
		if version == "(devel)" || strings.Contains(version, "-qa.") && !engine.QATrust {
			want = scaffold.FallbackSDKVersion
		}
		if got := sdkRequirementVersion(); got != want {
			t.Errorf("build %q (QA=%v) pins %q, want %q", version, engine.QATrust, got, want)
		}
	}
}

func TestScaffoldTidyResolvesQAPin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Go uses a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\ngo 1.26\nrequire github.com/sparkwing-dev/sparkwing v0.68.0-qa.16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$*" in
  'env GOTOOLCHAIN GOVERSION GOENV') printf 'auto\ngo1.26.9\noff\n';;
  'env -json GOPROXY GONOSUMDB GOPRIVATE') printf '{"GOPROXY":"https://proxy.golang.org,direct","GONOSUMDB":"","GOPRIVATE":""}\n';;
  'mod tidy') printf '%s\n%s\n' "$GOPROXY" "$GONOSUMDB" > seen;;
  *) exit 9;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("GOPROXY", "https://proxy.golang.org,direct")
	t.Setenv("GONOSUMDB", "")
	if ran, err := tidySkeleton(t.Context(), dir); !ran || err != nil {
		t.Fatalf("tidy = %v, %v", ran, err)
	}
	seen, err := os.ReadFile(filepath.Join(dir, "seen"))
	if err != nil {
		t.Fatal(err)
	}
	want := "https://proxy.golang.org,direct\n\n"
	if engine.QATrust {
		want = "https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct\ngithub.com/sparkwing-dev/sparkwing\n"
	}
	if string(seen) != want {
		t.Fatalf("tidy module settings = %q, want %q", seen, want)
	}
}
