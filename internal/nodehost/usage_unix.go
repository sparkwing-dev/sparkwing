//go:build unix

package nodehost

import (
	"os"
	"runtime"
	"syscall"
)

func maxRSSBytes(state *os.ProcessState) int64 {
	ru, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil || ru.Maxrss <= 0 {
		return 0
	}
	// safety: Linux reports ru_maxrss in KiB; macOS and the BSDs report bytes.
	if runtime.GOOS == "linux" {
		return ru.Maxrss * 1024
	}
	return ru.Maxrss
}
