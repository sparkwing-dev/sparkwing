package cache

import (
	"os"
	"syscall"
	"time"
)

func lastRead(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)) //nolint:unconvert // the fields are int32 on 32-bit platforms
	}
	return info.ModTime()
}
