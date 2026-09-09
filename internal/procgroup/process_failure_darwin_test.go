//go:build darwin

package procgroup

import (
	"errors"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestProcessTablePreservesKernelFailure(t *testing.T) {
	original := darwinProcessListing
	t.Cleanup(func() { darwinProcessListing = original })
	darwinProcessListing = func() ([]byte, error) { return nil, syscall.EIO }
	if _, err := processTable(true); !errors.Is(err, syscall.EIO) {
		t.Fatalf("process table error = %v, want kernel I/O failure", err)
	}
}

func TestProcessTableRejectsIncompleteKernelRecords(t *testing.T) {
	original := darwinProcessListing
	t.Cleanup(func() { darwinProcessListing = original })
	size := int(unsafe.Sizeof(unix.KinfoProc{}))
	for _, length := range []int{0, 1, size - 1, size + 1} {
		darwinProcessListing = func() ([]byte, error) { return make([]byte, length), nil }
		if _, err := processTable(true); err == nil {
			t.Errorf("accepted %d bytes for %d-byte kernel records", length, size)
		}
	}
}
