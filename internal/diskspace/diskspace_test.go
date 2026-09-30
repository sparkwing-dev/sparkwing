package diskspace_test

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/diskspace"
)

func TestUsage_DescribesTheVolumeHoldingThePath(t *testing.T) {
	free, total, ok := diskspace.Usage(t.TempDir())
	if !ok {
		t.Fatal("Usage could not read the volume a temp dir was just created on")
	}
	// safety: a mounted filesystem always spends something on its own
	// metadata, so equality would mean both numbers came from one source.
	if total == 0 || free >= total {
		t.Fatalf("Usage = %d free of %d total, which describes no real volume", free, total)
	}
}

func TestUsage_RefusesAPathItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-created-this")
	for _, path := range []string{"", missing} {
		free, total, ok := diskspace.Usage(path)
		if ok {
			t.Errorf("Usage(%q) reported ok with %d free of %d total; an unreadable path has no volume to describe", path, free, total)
		}
	}
}
