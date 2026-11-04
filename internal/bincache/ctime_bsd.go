//go:build darwin || freebsd || netbsd

package bincache

import (
	"syscall"
	"time"
)

func changeTime(stat *syscall.Stat_t) time.Time {
	return time.Unix(stat.Ctimespec.Unix())
}
