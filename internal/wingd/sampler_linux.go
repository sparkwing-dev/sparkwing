//go:build linux

package wingd

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// linuxClockTicks is the kernel's USER_HZ. It is fixed at 100 on every
// mainstream Linux build, so the stall heuristic reads /proc directly
// rather than pulling in a sysconf dependency for a threshold check.
const linuxClockTicks = 100.0

// sample derives the CPU of a holder's whole process tree as a fraction
// of one core from the change in cumulative user+system time between two
// /proc readings, summed over the holder and every descendant. Reading
// only the holder's own pid misses work that runs in forked children, so
// a busy holder driving child processes would read idle. The first
// reading for the holder's pid has no baseline and reports not-sampled.
func (p *procSampler) sample(pid int) (float64, bool) {
	cpu, children, ok := readProcTree()
	if !ok {
		return 0, false
	}
	if _, alive := cpu[pid]; !alive {
		p.mu.Lock()
		p.pruneLocked(cpu)
		p.mu.Unlock()
		return 0, false
	}
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	var frac float64
	rootPrimed := false
	for _, spid := range collectSubtree(pid, children) {
		cur := cpu[spid]
		prev, had := p.last[spid]
		p.last[spid] = cpuSample{cpuSeconds: cur, at: now}
		if !had {
			continue
		}
		if spid == pid {
			rootPrimed = true
		}
		wall := now.Sub(prev.at).Seconds()
		if wall <= 0 {
			continue
		}
		if d := (cur - prev.cpuSeconds) / wall; d > 0 {
			frac += d
		}
	}
	p.pruneLocked(cpu)
	if !rootPrimed {
		return 0, false
	}
	return frac, true
}

// pruneLocked drops baselines for pids no longer present so the map does
// not grow without bound as runs and their children come and go.
//
// safety: callers hold p.mu.
func (p *procSampler) pruneLocked(live map[int]float64) {
	for pid := range p.last {
		if _, ok := live[pid]; !ok {
			delete(p.last, pid)
		}
	}
}

// readProcTree enumerates every process under /proc and returns each
// pid's cumulative CPU seconds alongside a parent->children map for
// subtree walks.
func readProcTree() (map[int]float64, map[int][]int, bool) {
	dir, err := os.Open("/proc")
	if err != nil {
		return nil, nil, false
	}
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return nil, nil, false
	}
	cpu := make(map[int]float64, len(names))
	children := make(map[int][]int, len(names))
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		ppid, secs, ok := readProcStat(pid)
		if !ok {
			continue
		}
		cpu[pid] = secs
		children[ppid] = append(children[ppid], pid)
	}
	return cpu, children, true
}

// readProcStat parses a single /proc/<pid>/stat line into its parent pid
// and cumulative user+system CPU seconds. It splits after the comm field
// (parenthesized and possibly containing spaces) so the numeric fields
// line up regardless of the process name.
func readProcStat(pid int) (int, float64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, false
	}
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return 0, 0, false
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < 13 {
		return 0, 0, false
	}
	ppid, err0 := strconv.Atoi(fields[1])
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err0 != nil || err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return ppid, (utime + stime) / linuxClockTicks, true
}

func sampleHost() (HostStat, error) {
	stat := HostStat{TotalCores: float64(runtime.NumCPU())}

	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return stat, err
	}
	unit := uint64(si.Unit)
	if unit == 0 {
		unit = 1
	}
	stat.TotalMemoryBytes = uint64(si.Totalram) * unit
	stat.FreeMemoryBytes = uint64(si.Freeram) * unit
	stat.LoadAverage = float64(si.Loads[0]) / 65536.0

	if avail, ok := readMemAvailable(); ok {
		stat.FreeMemoryBytes = avail
	}
	return stat, nil
}

func readMemAvailable() (uint64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !bytes.HasPrefix([]byte(line), []byte("MemAvailable:")) {
			continue
		}
		fields := bytes.Fields([]byte(line))
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
