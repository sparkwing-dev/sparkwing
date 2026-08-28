package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstHostingReleaseIsReadable(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("locate repo root: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(root, firstHostingReleaseSource))
	if err != nil {
		t.Fatalf("read %s: %v", firstHostingReleaseSource, err)
	}
	m := firstHostingReleasePattern.FindSubmatch(src)
	if m == nil {
		t.Fatalf("no FirstHostingRelease declaration found in %s; the release gate would pass without checking it, "+
			"which is how a shipped constant ends up naming a release that never carried daemon hosting",
			firstHostingReleaseSource)
	}
	if got := string(m[1]); got == "" {
		t.Fatal("FirstHostingRelease parsed as empty")
	}
}
