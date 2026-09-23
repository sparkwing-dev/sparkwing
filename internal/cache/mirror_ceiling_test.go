package cache

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

// A mirror clone writes into the store the ceiling measures, so the count
// takes it in once the clone finishes rather than at the next scheduled
// measurement.
func TestAMirrorCloneReachesTheStoreCount(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	upstream := filepath.Join(t.TempDir(), "upstream")
	for _, args := range [][]string{
		{"init", "-q", upstream},
		{"-C", upstream, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "one"},
	} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
		}
	}
	// safety: registered before the fixture so it runs after the fixture's
	// cleanup has waited out the measurement that walks repoDir.
	savedRepo := repoDir
	repoDir = t.TempDir()
	t.Cleanup(func() { repoDir = savedRepo })
	ceilingFixture(t, objectguard.CeilingConfig{Limit: objectguard.CeilingLimit{MaxBytes: 1 << 30}, Reconcile: time.Hour})
	measureStore(t.Context())
	before := storeCeiling.State().Bytes

	if out, err := cloneMirror(upstream, filepath.Join(repoDir, "upstream.git")); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for storeCeiling.State().Bytes == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := storeCeiling.State().Bytes; got <= before {
		t.Fatalf("store count after a mirror clone = %d, want it past the %d before", got, before)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "upstream.git", "HEAD")); err != nil {
		t.Fatalf("the clone left no mirror: %v", err)
	}
}
