//go:build linux

package wingd

import (
	"os"
	"testing"
)

func TestProcessMetricsLinuxMeasuresResidentMemoryOnFirstSample(t *testing.T) {
	p := newProcSampler()
	p.CPUUsage(os.Getpid())
	usage := p.ProcessUsage(os.Getpid())
	if usage == nil || !usage.MemoryMeasured || usage.CurrentMemoryBytes == 0 {
		t.Fatalf("resident memory is unmeasured: %+v", usage)
	}
	if usage.CPUMeasured {
		t.Fatalf("first sample reported CPU without a baseline: %+v", usage)
	}
}
