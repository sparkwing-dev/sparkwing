//go:build !windows

package wingd

import (
	"os"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

type heartbeatWriter struct {
	data []byte
}

func openHeartbeat(path string) (*heartbeatWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := f.Truncate(heartbeatSize); err != nil {
		return nil, err
	}
	data, err := unix.Mmap(int(f.Fd()), 0, heartbeatSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &heartbeatWriter{data: data}, nil
}

func (h *heartbeatWriter) write(counter uint64) error {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&h.data[0])), counter)
	return nil
}

func (h *heartbeatWriter) close() error { return unix.Munmap(h.data) }
