package protocolgo

import (
	"errors"
	"syscall"
)

func childProtocolPipeOpen(handle uintptr) (bool, error) {
	kind, err := syscall.GetFileType(syscall.Handle(handle))
	if errors.Is(err, syscall.Errno(6)) {
		return false, nil
	}
	return err == nil && kind == syscall.FILE_TYPE_PIPE, err
}
