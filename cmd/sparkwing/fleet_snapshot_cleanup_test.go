package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchFleetCompileFailureCleansExactSource(t *testing.T) {
	tmp := t.TempDir()
	repo := initSnapshotRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, repo, ".sparkwing/go.mod", "module example.test/broken\n\ngo 1.26\n", 0o644)
	writeSnapshotFile(t, repo, ".sparkwing/main.go", "package main\nfunc main() { this is not Go }\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "broken pipeline")
	configPath := filepath.Join(tmp, "fleet.yaml")
	if err := os.WriteFile(configPath, []byte("listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nexecutors: [{name: helper, location: local}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDispatchFleetCompileFailureCleansExactSourceHelper$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_FLEET_COMPILE_FAILURE=1",
		"SPARKWING_TEST_FLEET_REPO="+repo,
		"SPARKWING_FLEET_CONFIG="+configPath,
		"SPARKWING_NO_BINCACHE=1",
		"GOWORK=off",
		"TMPDIR="+tmp,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("broken pipeline subprocess succeeded")
	}
	if text := string(out); strings.Contains(text, "no enrolled helpers") || !strings.Contains(text, "broken") || !strings.Contains(text, fleetUntrackedSourceWarning) {
		t.Fatalf("subprocess did not reach broken pipeline compilation: %s", text)
	}
	matches, err := filepath.Glob(filepath.Join(tmp, "sparkwing-worktree-snapshot-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("compile failure leaked exact source: %v", matches)
	}
}

func TestDispatchFleetCompileFailureCleansExactSourceHelper(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_FLEET_COMPILE_FAILURE") != "1" {
		return
	}
	err := dispatchRun([]string{"broken", "--sw-fleet", "--sw-dry-run", "--sw-cd", os.Getenv("SPARKWING_TEST_FLEET_REPO")})
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
