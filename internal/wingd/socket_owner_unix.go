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

func socketBaseFault(info fs.FileInfo) string {
	// safety: without the sticky bit any account that can write the base can
	// rename this user's socket directory away and substitute its own.
	if info.Mode()&os.ModeSticky != 0 {
		return ""
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Sprintf("mode %#o and no sticky bit", perm)
	}
	return ""
}

func socketDirReapable(info fs.FileInfo) bool {
	return socketDirFault(info) == ""
}
