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

// sample sums the decaying CPU-percentage estimate of a holder's whole
// process tree -- the holder and every descendant -- into a fraction of
// one core. Reading only the holder's own pid misses work that runs in
// forked children, so a busy holder driving child processes would read
// idle. It reports not-sampled when the holder is gone or the sysctl
// buffer is too short to hold one kinfo_proc.
func (p *procSampler) sample(pid int) (float64, bool) {
	raw, err := unix.SysctlRaw("kern.proc.all")
	if err != nil {
		return 0, false
	}
	stride := int(unsafe.Sizeof(unix.KinfoProc{}))
	if stride == 0 || len(raw) < stride {
		return 0, false
	}
	pct := make(map[int]float64, len(raw)/stride)
	children := make(map[int][]int, len(raw)/stride)
	found := false
	for off := 0; off+stride <= len(raw); off += stride {
		kp := (*unix.KinfoProc)(unsafe.Pointer(&raw[off]))
		cpid := int(kp.Proc.P_pid)
		pct[cpid] = float64(kp.Proc.P_pctcpu) / float64(darwinFScale)
		children[int(kp.Eproc.Ppid)] = append(children[int(kp.Eproc.Ppid)], cpid)
		if cpid == pid {
			found = true
		}
	}
	if !found {
		return 0, false
	}
	var frac float64
	for _, spid := range collectSubtree(pid, children) {
		frac += pct[spid]
	}
	return frac, true
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
