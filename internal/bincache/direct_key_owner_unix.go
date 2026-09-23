//go:build !windows

package bincache

import (
	"os"
	"syscall"
)

func ownedByThisUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
