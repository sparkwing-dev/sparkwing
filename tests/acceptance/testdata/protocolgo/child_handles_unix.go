//go:build unix

package protocolgo

import (
	"errors"
	"syscall"
)

func childProtocolPipeOpen(handle uintptr) (bool, error) {
	var stat syscall.Stat_t
	err := syscall.Fstat(int(handle), &stat)
	if errors.Is(err, syscall.EBADF) {
		return false, nil
	}
	return err == nil && stat.Mode&syscall.S_IFMT == syscall.S_IFIFO, err
}
