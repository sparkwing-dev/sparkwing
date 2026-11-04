//go:build !windows

package bincache

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

var stableAfter = 2 * time.Second

// safety: the change time cannot be set by a user, so a rewrite that restores the modification time still changes
// the identity; a file changed within the clock's granularity of now is not yet stable.
func fileIdentity(info os.FileInfo, now time.Time) (string, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return "", false
	}
	changed := time.Unix(int64(stat.Ctim.Sec), int64(stat.Ctim.Nsec))
	if now.Sub(changed) < stableAfter || now.Sub(info.ModTime()) < stableAfter {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano(), changed.UnixNano()), true
}
