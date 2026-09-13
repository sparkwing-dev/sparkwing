package wingd

import (
	"strconv"
	"strings"
	"time"
)

type cpuTotals struct {
	busy  float64
	total float64
}

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

func busyCoresFromTotals(prev, cur cpuTotals, totalCores float64) (float64, bool) {
	totalDelta := cur.total - prev.total
	busyDelta := cur.busy - prev.busy
	if totalDelta <= 0 || busyDelta < 0 || totalCores <= 0 {
		return 0, false
	}
	return clampCores(busyDelta/totalDelta*totalCores, totalCores), true
}

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

func clampCores(cores, totalCores float64) float64 {
	if cores < 0 {
		return 0
	}
	if cores > totalCores {
		return totalCores
	}
	return cores
}

func parseProcUptime(data string) (float64, bool) {
	fields := strings.Fields(data)
	if len(fields) == 0 {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds <= 0 {
		return 0, false
	}
	return seconds, true
}

func processStartFromCreation(now, createdAt time.Time) time.Time {
	// safety: the returned time keeps now's monotonic reading, which is what a
	// later comparison against the scan bounds needs. A creation stamp is a wall
	// value, and comparing one against a monotonic reading drops both sides to the
	// wall clock, where a step larger than the dating slack moves the bound and
	// nothing goes red.
	age := now.Sub(createdAt)
	if age < 0 {
		return time.Time{}
	}
	return now.Add(-age)
}
