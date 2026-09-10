//go:build !windows

package toolchain

import (
	"os"

	"golang.org/x/sys/unix"
)

func openHoldFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}
