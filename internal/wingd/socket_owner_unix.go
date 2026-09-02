//go:build !windows

package wingd

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func socketDirOwnedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func socketDirFault(info fs.FileInfo) string {
	if !socketDirOwnedByCurrentUser(info) {
		return "owned by another user"
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		return fmt.Sprintf("mode %#o, want 0700", perm)
	}
	return ""
}
