package capacity

import "github.com/sparkwing-dev/sparkwing/pkg/store"

// SampledResources describes available interval observations, not lifetime totals.
type SampledResources struct {
	PeakCores       *float64
	SustainedCores  *float64
	PeakMemoryBytes *int64
}

func SummarizeIntervals(samples []store.MetricSample) SampledResources {
	var result SampledResources
	var cores []float64
	cpuGap, memoryGap := false, false
	for _, sample := range samples {
		if sample.Kind == store.MetricCommand {
			continue
		}
		if sample.Kind != store.MetricInterval {
			cpuGap, memoryGap = true, true
			continue
		}
		cpuGap = cpuGap || !sample.CPUAvailable
		memoryGap = memoryGap || !sample.MemoryAvailable
		if sample.CPUAvailable {
			value := float64(sample.CPUMillicores) / 1000
			cores = append(cores, value)
			if result.PeakCores == nil || value > *result.PeakCores {
				result.PeakCores = new(value)
			}
		}
		if sample.MemoryAvailable && (result.PeakMemoryBytes == nil || sample.MemoryBytes > *result.PeakMemoryBytes) {
			result.PeakMemoryBytes = new(sample.MemoryBytes)
		}
	}
	if len(cores) > 0 {
		result.SustainedCores = new(store.NearestRankPercentile(cores, SustainedPercentile))
	}
	if cpuGap {
		result.PeakCores = nil
		result.SustainedCores = nil
	}
	if memoryGap {
		result.PeakMemoryBytes = nil
	}
	return result
}
