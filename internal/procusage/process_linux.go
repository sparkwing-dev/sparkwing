//go:build linux

package procusage

import (
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

func (t *Tree) Read() Snapshot {
	return t.readLinux("/proc", residentMemory)
}

func (t *Tree) readLinux(directory string, readRSS func(string) (int64, bool)) Snapshot {
	snapshot := Snapshot{Start: time.Now(), Processes: make(map[int]Process)}
	entries, err := os.ReadDir(directory)
	frequency := clockTicks()
	if err == nil {
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 0 {
				continue
			}
			raw, err := os.ReadFile(directory + "/" + entry.Name() + "/stat")
			if err != nil {
				snapshot.Processes[pid] = Process{Identity: Identity{PID: pid}, ReadAt: time.Now()}
				continue
			}
			process := parseStat(string(raw), frequency)
			if process.PID != pid {
				process = Process{Identity: Identity{PID: pid}}
			}
			process.ReadAt = time.Now()
			snapshot.Processes[pid] = process
		}
		snapshot.Available = true
		selected, _ := t.selectProcesses(snapshot)
		for pid := range selected {
			process := snapshot.Processes[pid]
			path := directory + "/" + strconv.Itoa(pid)
			process.RSS, process.RSSAvailable = readRSS(path)
			afterRaw, afterErr := os.ReadFile(path + "/stat")
			after := parseStat(string(afterRaw), frequency)
			if afterErr != nil || !after.IdentityAvailable || after.Identity != process.Identity || after.Parent != process.Parent {
				process.AncestryAvailable = false
				process.CPUAvailable, process.ReapedAvailable, process.RSSAvailable = false, false, false
			}
			snapshot.Processes[pid] = process
		}
	}
	snapshot.End = time.Now()
	return snapshot
}

func clockTicks() uint64 {
	raw, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return 0
	}
	width := strconv.IntSize / 8
	word := func(value []byte) uint64 {
		if width == 8 {
			return binary.NativeEndian.Uint64(value)
		}
		return uint64(binary.NativeEndian.Uint32(value))
	}
	for offset := 0; offset+2*width <= len(raw); offset += 2 * width {
		if word(raw[offset:]) == 17 {
			return word(raw[offset+width:])
		}
	}
	return 0
}

func parseStat(raw string, frequency uint64) Process {
	open, close := strings.IndexByte(raw, '('), strings.LastIndexByte(raw, ')')
	if open < 1 || close < open {
		return Process{}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(raw[:open]))
	fields := strings.Fields(raw[close+1:])
	if err != nil || pid <= 0 || len(fields) < 22 {
		return Process{}
	}
	p := Process{Identity: Identity{PID: pid}}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return p
	}
	p.Parent = parent
	p.Birth, err = strconv.ParseUint(fields[19], 10, 64)
	p.IdentityAvailable = err == nil
	p.AncestryAvailable = p.IdentityAvailable
	readTime := func(a, b string) (time.Duration, bool) {
		first, errFirst := strconv.ParseUint(a, 10, 64)
		second, errSecond := strconv.ParseUint(b, 10, 64)
		if errFirst != nil || errSecond != nil || first > math.MaxUint64-second {
			return 0, false
		}
		return ticksDuration(first+second, frequency)
	}
	p.CPU, p.CPUAvailable = readTime(fields[11], fields[12])
	p.ReapedCPU, p.ReapedAvailable = readTime(fields[13], fields[14])
	return p
}

func residentMemory(directory string) (int64, bool) {
	raw, err := os.ReadFile(directory + "/smaps_rollup")
	if err == nil {
		return parseResidentMemory(string(raw), false)
	}
	if !os.IsNotExist(err) {
		return 0, false
	}
	raw, err = os.ReadFile(directory + "/smaps")
	if err != nil {
		return 0, false
	}
	return parseResidentMemory(string(raw), true)
}

func parseResidentMemory(raw string, multiple bool) (int64, bool) {
	var total int64
	var found bool
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Rss:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" || found && !multiple {
			return 0, false
		}
		kib, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kib < 0 || kib > (math.MaxInt64-total)/1024 {
			return 0, false
		}
		total += kib * 1024
		found = true
	}
	return total, found
}
