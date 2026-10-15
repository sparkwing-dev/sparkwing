package sdkserve_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPodNodeFixtureBuilds(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate fixture build test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	cmd := exec.CommandContext(t.Context(), "go", "-C", root, "build", "-p", "4", "-o", filepath.Join(t.TempDir(), "podnodefixture"), "./internal/orchestrator/testdata/podnodefixture")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build retained pipeline fixture: %v\n%s", err, output)
	}
}
