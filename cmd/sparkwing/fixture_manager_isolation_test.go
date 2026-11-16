package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testmanager"
)

func TestReleaseBuildExcludesUserManagerFixtureInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("builds fixture and release-style CLIs; full suite retains this check")
	}
	fixture, err := os.ReadFile(buildSubmitCLI(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(fixture, []byte(fixtureUserManagerMarker)) == testmanager.Enabled() {
		t.Fatal("fixture backend does not match the explicit manager selection")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "sparkwing")
	cmd := exec.CommandContext(t.Context(), "go", "-C", root, "build", "-o", binary, "./cmd/sparkwing")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("release-style build: %v: %s", err, out)
	}
	release, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(release, []byte(fixtureUserManagerMarker)) {
		t.Fatal("release build contains the unmanaged test fixture")
	}
}
