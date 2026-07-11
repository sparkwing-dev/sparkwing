//go:build darwin

package wingd

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// darwinFScale is the fixed-point scale (1<<FSHIFT, FSHIFT=11) that the
// kernel applies to ExternProc.P_pctcpu. Dividing by it yields the
// process's CPU as a fraction of one core, the same figure ps derives
// for its %CPU column.
const darwinFScale = 1 << 11

// sample reads the process's decaying CPU-percentage estimate from its
// kinfo_proc and converts it to a fraction of one core. It reports
// not-sampled when the process is gone or the sysctl buffer is short.
func (p *procSampler) sample(pid int) (float64, bool) {
	raw, err := unix.SysctlRaw("kern.proc.pid", pid)
	if err != nil || len(raw) < int(unsafe.Sizeof(unix.KinfoProc{})) {
		return 0, false
	}
	kp := (*unix.KinfoProc)(unsafe.Pointer(&raw[0]))
	return float64(kp.Proc.P_pctcpu) / float64(darwinFScale), true
}

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

	// hack: page_free_count counts only truly free pages, so on macOS -- which
	// parks most of RAM in reclaimable cache and the compressor -- it reads as
	// near-zero even with gigabytes available, which would pin memory headroom
	// at zero and wedge every memory-costed admission. memorystatus_level is
	// the kernel's own percent-available figure (the darwin analog of Linux
	// MemAvailable), so prefer it when sane.
	if level, err := unix.SysctlUint32("kern.memorystatus_level"); err == nil && level > 0 && level <= 100 {
		stat.FreeMemoryBytes = uint64(float64(stat.TotalMemoryBytes) * float64(level) / 100.0)
	}

	if stat.TotalMemoryBytes == 0 {
		return stat, fmt.Errorf("wingd: hw.memsize unavailable")
	}
	return stat, nil
}
