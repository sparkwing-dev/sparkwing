//go:build darwin

package wingd

import (
	"encoding/binary"
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

func sampleHost() (HostStat, error) {
	stat := HostStat{TotalCores: float64(runtime.NumCPU())}

	if mem, err := unix.SysctlUint64("hw.memsize"); err == nil {
		stat.TotalMemoryBytes = mem
	}

	if raw, err := unix.SysctlRaw("vm.loadavg"); err == nil && len(raw) >= 24 {
		ldavg := binary.LittleEndian.Uint32(raw[0:4])
		fscale := binary.LittleEndian.Uint64(raw[16:24])
		if fscale > 0 {
			stat.LoadAverage = float64(ldavg) / float64(fscale)
		}
	}

	pageSize := uint64(unix.Getpagesize())
	if free, err := unix.SysctlUint32("vm.page_free_count"); err == nil {
		stat.FreeMemoryBytes = uint64(free) * pageSize
	}
	if spec, err := unix.SysctlUint32("vm.page_speculative_count"); err == nil {
		stat.FreeMemoryBytes += uint64(spec) * pageSize
	}

	if stat.TotalMemoryBytes == 0 {
		return stat, fmt.Errorf("wingd: hw.memsize unavailable")
	}
	return stat, nil
}
