package capacity

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSummarizeIntervalsSeparatesQuantities(t *testing.T) {
	for _, sample := range []store.MetricSample{
		{Kind: store.MetricCommand, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 1000, CPUTime: 120 * time.Second, MemoryBytes: 4096},
		{Kind: store.MetricCommand, CPUAvailable: true, MemoryAvailable: true},
		{Kind: store.MetricEstimate, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 1000, MemoryBytes: 4096},
		{CPUMillicores: 1000, MemoryBytes: 4096},
		{Kind: store.MetricInterval, CPUMillicores: 1000, MemoryBytes: 4096},
	} {
		got := SummarizeIntervals([]store.MetricSample{sample})
		if got.PeakCores != nil || got.SustainedCores != nil || got.PeakMemoryBytes != nil {
			t.Errorf("non-measurement promoted: input=%+v output=%+v", sample, got)
		}
	}
}

func TestSummarizeIntervalsKeepsIndependentZero(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cpu, memory bool
	}{
		{"both", true, true}, {"CPU", true, false}, {"memory", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeIntervals([]store.MetricSample{{Kind: store.MetricInterval, CPUAvailable: tc.cpu, MemoryAvailable: tc.memory}})
			if (got.PeakCores != nil) != tc.cpu || (got.SustainedCores != nil) != tc.cpu || (got.PeakMemoryBytes != nil) != tc.memory {
				t.Fatalf("availability changed: %+v", got)
			}
			if tc.cpu && (*got.PeakCores != 0 || *got.SustainedCores != 0) {
				t.Fatal("measured idle CPU changed")
			}
			if tc.memory && *got.PeakMemoryBytes != 0 {
				t.Fatal("measured zero memory changed")
			}
		})
	}
}

func TestSummarizeIntervalsPercentileIsNotMean(t *testing.T) {
	var samples []store.MetricSample
	for range 9 {
		samples = append(samples, store.MetricSample{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true, MemoryBytes: 4096})
	}
	samples = append(samples, store.MetricSample{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 1000, MemoryBytes: 8192})
	got := SummarizeIntervals(samples)
	if got.PeakCores == nil || *got.PeakCores != 1 || got.SustainedCores == nil || *got.SustainedCores != 0 || got.PeakMemoryBytes == nil || *got.PeakMemoryBytes != 8192 {
		t.Fatalf("interval quantities changed: %+v", got)
	}
}

func TestSummarizeIntervalsRejectsKnownGaps(t *testing.T) {
	for _, cpuGap := range []bool{false, true} {
		good := store.MetricSample{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 1000, MemoryBytes: 4096}
		gap := good
		gap.CPUAvailable = !cpuGap
		gap.MemoryAvailable = cpuGap
		got := SummarizeIntervals([]store.MetricSample{good, gap, good})
		if (got.PeakCores == nil) != cpuGap || (got.SustainedCores == nil) != cpuGap || (got.PeakMemoryBytes == nil) == cpuGap {
			t.Fatalf("gap was hidden by valid intervals: %+v", got)
		}
		if cpuGap && *got.PeakMemoryBytes != 4096 {
			t.Errorf("valid memory = %d, want 4096", *got.PeakMemoryBytes)
		}
		if !cpuGap && (*got.PeakCores != 1 || *got.SustainedCores != 1) {
			t.Errorf("valid CPU = %v/%v, want 1/1", *got.PeakCores, *got.SustainedCores)
		}
	}
}

func TestSummarizeIntervalsRejectsMixedEvidence(t *testing.T) {
	good := store.MetricSample{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 1000, MemoryBytes: 4096}
	for _, kind := range []store.MetricKind{"", store.MetricEstimate} {
		gap := store.MetricSample{Kind: kind, CPUMillicores: 2000, MemoryBytes: 8192}
		got := SummarizeIntervals([]store.MetricSample{good, gap, good})
		if got.PeakCores != nil || got.SustainedCores != nil || got.PeakMemoryBytes != nil {
			t.Errorf("mixed %q evidence qualified: %+v", kind, got)
		}
	}
}
