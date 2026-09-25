package bincache

import "syscall"

const tmpfsMagic = 0x01021994

func isTmpfs(dir string) bool {
	var st syscall.Statfs_t
	return syscall.Statfs(dir, &st) == nil && st.Type == tmpfsMagic
}
