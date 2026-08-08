package wingd

import (
	"strconv"
	"strings"
)

// cpuTotals is one reading of a machine's cumulative CPU time, split into
// the part spent executing instructions and the whole span the counters
// cover. Both are in whatever unit the platform counts in; only their
// ratio between two readings is ever used, so the unit cancels.
type cpuTotals struct {
	busy  float64
	total float64
}

// parseProcStatCPU reads the aggregate "cpu" line of Linux /proc/stat into
// cumulative totals. Idle and iowait are counted toward total but not
// toward busy: a thread parked on uninterruptible I/O holds no core, and
// counting it as consumption is exactly what makes an I/O-heavy box look
// full while its CPUs sit idle. It reports false when the line is absent
// or too short to carry the fields through steal.
func parseProcStatCPU(data string) (cpuTotals, bool) {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] != "cpu" {
			continue
		}
		var totals cpuTotals
		for index, field := range fields[1:9] {
			ticks, err := strconv.ParseFloat(field, 64)
			if err != nil {
				return cpuTotals{}, false
			}
			totals.total += ticks
			// safety: fields 3 and 4 of the value slice are idle and iowait,
			// the two spans where no instruction retires.
			if index != 3 && index != 4 {
				totals.busy += ticks
			}
		}
		return totals, true
	}
	return cpuTotals{}, false
}

// busyCoresFromTotals converts two cumulative readings into cores busy over
// the span between them. It reports false when the span is empty or the
// counters moved backwards, which is what a first reading, a repeated
// reading, and a counter reset all look like -- none of them a measurement
// of an idle machine.
func busyCoresFromTotals(prev, cur cpuTotals, totalCores float64) (float64, bool) {
	totalDelta := cur.total - prev.total
	busyDelta := cur.busy - prev.busy
	if totalDelta <= 0 || busyDelta < 0 || totalCores <= 0 {
		return 0, false
	}
	return clampCores(busyDelta/totalDelta*totalCores, totalCores), true
}

// sumProcessCPUPercent sums a column of per-process CPU percentages, each
// relative to one core, and returns the total in cores. It reports false
// for output carrying no parsable row, so an empty read from a process
// table that could not be listed never passes as an idle machine. A row
// that does not parse is skipped rather than failing the whole reading:
// the process table is enumerated live and races the processes in it.
func sumProcessCPUPercent(out string, totalCores float64) (float64, bool) {
	var percent float64
	var rows int
	for _, line := range strings.Split(out, "\n") {
		field := strings.TrimSpace(line)
		if field == "" {
			continue
		}
		value, err := strconv.ParseFloat(field, 64)
		if err != nil || value < 0 {
			continue
		}
		percent += value
		rows++
	}
	if rows == 0 {
		return 0, false
	}
	return clampCores(percent/100.0, totalCores), true
}

// clampCores holds a derived core figure inside the machine it describes.
// Sampling races and per-process percentages that each round up can carry
// a sum past the core count, and a busy figure above capacity would drive
// admission headroom negative on a box that is merely fully used.
func clampCores(cores, totalCores float64) float64 {
	if cores < 0 {
		return 0
	}
	if cores > totalCores {
		return totalCores
	}
	return cores
}
