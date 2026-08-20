package wingd_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureCleanupChildEnv = "SPARKWING_FIXTURE_CLEANUP_CHILD"

func TestBuildFixtureProcessRemovesTemporaryDirectory(t *testing.T) {
	if marker := os.Getenv(fixtureCleanupChildEnv); marker != "" {
		dir := filepath.Dir(buildFixture(t))
		if err := os.WriteFile(marker, []byte(dir), 0o600); err != nil {
			t.Fatalf("write fixture directory marker: %v", err)
		}
		return
	}

	marker := filepath.Join(t.TempDir(), "fixture-dir")
	cmd := exec.Command(os.Args[0], "-test.run", "^TestBuildFixtureProcessRemovesTemporaryDirectory$")
	cmd.Env = append(os.Environ(), fixtureCleanupChildEnv+"="+marker)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture child: %v\n%s", err, out)
	}
	dirBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read fixture directory marker: %v", err)
	}
	dir := strings.TrimSpace(string(dirBytes))
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared fixture directory %q remains after the test process: %v", dir, err)
	}
}
