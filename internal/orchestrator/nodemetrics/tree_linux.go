package nodemetrics

import (
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

func processSnapshot(root int) (map[int]processSample, bool) {
	entries, err := os.ReadDir("/proc")
	frequency := processClockTicks()
	if err != nil || frequency == 0 {
		return nil, false
	}
	processes := make(map[int]processSample)
	zombies := make(map[int]bool)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		p, zombie, ok := parseProcessStat(string(raw), pid, frequency)
		if err != nil || !ok {
			return nil, false
		}
		processes[pid], zombies[pid] = p, zombie
	}
	for pid, p := range treeProcesses(root, processes) {
		if !zombies[pid] {
			raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/smaps_rollup")
			p.rss, p.valid = processResidentBytes(string(raw))
			p.valid = p.valid && err == nil
		}
		processes[pid] = p
	}
	return processes, true
}

func processClockTicks() uint64 {
	raw, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return 0
	}
	width := strconv.IntSize / 8
	word := func(raw []byte) uint64 {
		if width == 8 {
			return binary.NativeEndian.Uint64(raw)
		}
		return uint64(binary.NativeEndian.Uint32(raw))
	}
	for offset := 0; offset+2*width <= len(raw); offset += 2 * width {
		if word(raw[offset:]) == 17 {
			return word(raw[offset+width:])
		}
	}
	return 0
}

func parseProcessStat(raw string, pid int, frequency uint64) (processSample, bool, bool) {
	start, end := strings.IndexByte(raw, '('), strings.LastIndexByte(raw, ')')
	if start < 1 || end < start {
		return processSample{}, false, false
	}
	parsedPID, errPID := strconv.Atoi(strings.TrimSpace(raw[:start]))
	if errPID != nil || parsedPID != pid || pid <= 0 {
		return processSample{}, false, false
	}
	f := strings.Fields(raw[end+1:])
	if len(f) < 20 {
		return processSample{}, false, false
	}
	parent, err := strconv.Atoi(f[1])
	birth, birthErr := strconv.ParseUint(f[19], 10, 64)
	readCPU := func(a, b string) (time.Duration, bool) {
		x, errX := strconv.ParseUint(a, 10, 64)
		y, errY := strconv.ParseUint(b, 10, 64)
		if errX != nil || errY != nil || x > math.MaxUint64-y {
			return 0, false
		}
		return cpuTicks(x+y, frequency)
	}
	cpu, cpuOK := readCPU(f[11], f[12])
	reaped, reapedOK := readCPU(f[13], f[14])
	ok := err == nil && parent >= 0 && birthErr == nil && cpuOK && reapedOK
	return processSample{parent: parent, birth: birth, cpu: cpu, reaped: reaped, valid: ok}, f[0] == "Z", ok
}

func processResidentBytes(raw string) (int64, bool) {
	var rss int64
	found := false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "Rss:" {
			continue
		}
		if found || len(f) != 3 || f[2] != "kB" {
			return 0, false
		}
		kib, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || kib < 0 || kib > math.MaxInt64/1024 {
			return 0, false
		}
		rss, found = kib*1024, true
	}
	return rss, found
}
