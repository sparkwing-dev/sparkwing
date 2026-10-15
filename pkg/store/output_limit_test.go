package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func FuzzOutputReferenceUsesSharedLimit(f *testing.F) {
	for _, size := range []int64{0, 1, limits.NodeOutputBytes - 1, limits.NodeOutputBytes, limits.NodeOutputBytes + 1, 64 << 20} {
		f.Add(size)
	}
	f.Fuzz(func(t *testing.T, size int64) {
		ref := OutputRef{Key: "outputs/run/job/a1-g1-output", Size: size, SHA256: strings.Repeat("a", 64)}
		if got, want := ref.valid("run", "job"), size > 0 && size <= limits.NodeOutputBytes; got != want {
			t.Fatalf("size=%d valid=%v, want %v", size, got, want)
		}
	})
}

func TestOutputFileRejectsOversizedReferenceBeforeOpening(t *testing.T) {
	ref := OutputRef{Key: "outputs/run/job/a1-g1-output", Size: limits.NodeOutputBytes + 1, SHA256: strings.Repeat("a", 64)}
	value, err := ReadOutputFile(filepath.Join(t.TempDir(), "absent"), ref)
	if len(value) != 0 || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("value=%d bytes, err=%v", len(value), err)
	}
}
