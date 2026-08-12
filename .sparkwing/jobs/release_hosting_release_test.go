package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFirstHostingReleaseIsReadable pins the fragile half of the
// daemon-hosting release gate: it reads a constant out of Go source by
// regexp, because the pipeline module cannot import internal/. If the
// constant is renamed, reformatted, or moved, the gate would quietly stop
// checking anything -- so the parse is asserted here rather than
// discovered at tag time.
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
