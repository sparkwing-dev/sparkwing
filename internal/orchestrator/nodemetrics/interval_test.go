package nodemetrics

import (
	"runtime"
	"testing"
	"time"
)

// TestIntervalMillicores_ClampsReapBurstToHostCores pins the sampler clamp: a
// reaped subtree's cumulative CPU landing in one short interval would read as a
// rate no machine could sustain, so the derived rate caps at host cores; an
// in-range rate passes through and a non-positive interval draws nothing.
func TestIntervalMillicores_ClampsReapBurstToHostCores(t *testing.T) {
	host := int64(runtime.NumCPU()) * 1000

	burst := time.Duration(runtime.NumCPU()*100) * time.Second
	if got := intervalMillicores(burst, 2*time.Second); got != host {
		t.Errorf("reap-burst rate = %d, want clamped to host %d", got, host)
	}
	if got := intervalMillicores(2*time.Second, 2*time.Second); got != 1000 {
		t.Errorf("in-range rate = %d, want 1000", got)
	}
	if got := intervalMillicores(time.Second, 0); got != 0 {
		t.Errorf("zero interval = %d, want 0", got)
	}
	if got := intervalMillicores(-time.Second, 2*time.Second); got != 0 {
		t.Errorf("negative cpu = %d, want 0", got)
	}
}
