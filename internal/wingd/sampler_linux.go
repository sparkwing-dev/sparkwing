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

// sample derives a process's CPU as a fraction of one core from the
// change in its cumulative user+system time between two /proc readings.
// The first reading for a pid has no baseline and reports not-sampled.
func (p *procSampler) sample(pid int) (float64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		p.forget(pid)
		return 0, false
	}
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	cpuSeconds := (utime + stime) / linuxClockTicks
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	prev, ok := p.last[pid]
	p.last[pid] = cpuSample{cpuSeconds: cpuSeconds, at: now}
	if !ok {
		return 0, false
	}
	wall := now.Sub(prev.at).Seconds()
	if wall <= 0 {
		return 0, false
	}
	frac := (cpuSeconds - prev.cpuSeconds) / wall
	if frac < 0 {
		frac = 0
	}
	return frac, true
}

// forget drops a dead pid's baseline so the map does not grow without
// bound as runs come and go.
func (p *procSampler) forget(pid int) {
	p.mu.Lock()
	delete(p.last, pid)
	p.mu.Unlock()
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
