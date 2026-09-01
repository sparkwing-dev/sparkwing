package wingd

import (
	"strconv"
	"strings"
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
