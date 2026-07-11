//go:build linux

package wingd

import (
	"bufio"
	"bytes"
	"os"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

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
