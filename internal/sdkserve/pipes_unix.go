//go:build unix

package sdkserve

import (
	"fmt"
	"os"
	"syscall"
)

const (
	requestFD = 3
	frameFD   = 4
)

func openPipes() (req, frames *os.File, err error) {
	// safety: unset even on Unix, so no command this process starts sees a
	// handle list that names descriptors it does not hold.
	if err := os.Unsetenv(ProtocolHandlesEnv); err != nil {
		return nil, nil, err
	}
	for _, fd := range []int{requestFD, frameFD} {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return nil, nil, fmt.Errorf("descriptor %d is not open; only a sparkwing host starts --sw-node-protocol: %w", fd, err)
		}
		// safety: the host sees end of frames only when every holder of
		// descriptor 4 is gone, so no command pipeline code starts may hold it.
		syscall.CloseOnExec(fd)
	}
	return os.NewFile(requestFD, "sw-requests"), os.NewFile(frameFD, "sw-frames"), nil
}
