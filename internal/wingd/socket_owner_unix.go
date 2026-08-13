//go:build !windows

package wingd

import (
	"io/fs"
	"os"
	"syscall"
)

func socketDirOwnedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
