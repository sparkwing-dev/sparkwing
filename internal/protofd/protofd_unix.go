//go:build unix

package protofd

import (
	"fmt"
	"os"
	"syscall"
)

func open() (*os.File, *os.File, error) {
	if len(os.Args) != 2 {
		return nil, nil, fmt.Errorf("%s takes no handle arguments on Unix", Arg)
	}
	fds := []int{3, 4}
	for _, fd := range fds {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return nil, nil, fmt.Errorf("descriptor %d is not open; only a sparkwing host starts %s: %w", fd, Arg, err)
		}
		// safety: a descriptor that is not a pipe is not the host's, so it
		// keeps whatever inheritance its owner gave it.
		if st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			return nil, nil, fmt.Errorf("descriptor %d is not a pipe; only a sparkwing host starts %s", fd, Arg)
		}
	}
	for _, fd := range fds {
		syscall.CloseOnExec(fd)
	}
	return os.NewFile(3, "sw-requests"), os.NewFile(4, "sw-frames"), nil
}
