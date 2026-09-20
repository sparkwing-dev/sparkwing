package diskspace_test

import (
	"math/rand"
	"os"
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
	for _, path := range []string{"", "/no/such/volume/on/any/machine"} {
		free, total, ok := diskspace.Usage(path)
		if ok {
			t.Errorf("Usage(%q) reported ok with %d free of %d total; an unreadable path has no volume to describe", path, free, total)
		}
	}
}

// Reporting blocks where bytes are meant reads as a near-full disk on every
// healthy machine, whatever each platform multiplies by to get there.
func TestUsage_CountsBytesRatherThanBlocks(t *testing.T) {
	dir := t.TempDir()
	before, _, ok := diskspace.Usage(dir)
	if !ok {
		t.Fatal("Usage could not read the volume a temp dir was just created on")
	}

	// safety: written is large enough that the block-counted answer -- tens of
	// kilobytes at any usual block size -- falls far under floor, and that
	// concurrent deletions would have to free most of it to mask the write.
	const written = 128 << 20
	const floor = 8 << 20

	body := make([]byte, 1<<20)
	if _, err := rand.New(rand.NewSource(1)).Read(body); err != nil {
		t.Fatalf("fill buffer: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, "ballast"))
	if err != nil {
		t.Fatalf("create ballast: %v", err)
	}
	defer func() { _ = f.Close() }()
	for range written / len(body) {
		if _, err := f.Write(body); err != nil {
			t.Fatalf("write ballast: %v", err)
		}
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync ballast: %v", err)
	}

	after, _, ok := diskspace.Usage(dir)
	if !ok {
		t.Fatal("Usage stopped reading the volume after a file was written to it")
	}
	if before < after || before-after < floor {
		t.Errorf("writing %d bytes moved free space by %d; want at least %d, so the reading is in bytes",
			written, int64(before)-int64(after), floor)
	}
}
