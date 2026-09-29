//go:build !windows

package diskspace

import (
	"syscall"
	"testing"
)

func TestUsageConvertsAvailableBlocksToBytes(t *testing.T) {
	previous := statfs
	t.Cleanup(func() { statfs = previous })
	statfs = func(path string, st *syscall.Statfs_t) error {
		if path != "volume" {
			t.Fatalf("statfs path = %q, want volume", path)
		}
		st.Bsize = 4096
		st.Bavail = 3
		st.Bfree = 7
		st.Blocks = 16
		return nil
	}
	free, total, ok := Usage("volume")
	if !ok || free != 12288 || total != 65536 {
		t.Fatalf("Usage = %d, %d, %v; want 12288, 65536, true", free, total, ok)
	}
}

func TestUsageRejectsStatfsFailure(t *testing.T) {
	previous := statfs
	t.Cleanup(func() { statfs = previous })
	statfs = func(_ string, st *syscall.Statfs_t) error {
		st.Bsize = 4096
		st.Bavail = 3
		st.Blocks = 16
		return syscall.EIO
	}
	free, total, ok := Usage("volume")
	if ok || free != 0 || total != 0 {
		t.Fatalf("failed statfs produced usage: %d, %d, %v", free, total, ok)
	}
}
