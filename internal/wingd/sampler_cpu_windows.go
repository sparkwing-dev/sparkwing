//go:build windows

package wingd

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var getSystemTimes = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemTimes")

type cpuTracker struct {
	prev cpuTotals
	seen bool
}

func (c *cpuTracker) busyCores(totalCores float64) (float64, bool) {
	cur, ok := windowsCPUTotals()
	if !ok {
		return 0, false
	}
	previous, seen := c.prev, c.seen
	c.prev, c.seen = cur, true
	if !seen {
		return 0, false
	}
	return busyCoresFromTotals(previous, cur, totalCores)
}

func windowsCPUTotals() (cpuTotals, bool) {
	var idle, kernel, user windows.Filetime
	result, _, _ := getSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return cpuTotals{}, false
	}
	idleTicks := windowsFiletimeTicks(idle)
	totalTicks := windowsFiletimeTicks(kernel) + windowsFiletimeTicks(user)
	if idleTicks > totalTicks {
		return cpuTotals{}, false
	}
	return cpuTotals{busy: float64(totalTicks - idleTicks), total: float64(totalTicks)}, true
}
