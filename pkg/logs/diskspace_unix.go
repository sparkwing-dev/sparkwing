//go:build !windows

package logs

import "syscall"

func diskSpace(path string) (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	//nolint:unconvert // darwin statfs fields are int64; linux are uint64.
	return uint64(st.Bavail) * uint64(st.Bsize),
		uint64(st.Blocks) * uint64(st.Bsize), true
}
