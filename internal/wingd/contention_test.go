package wingd

import (
	"strings"
	"testing"
)

func TestContentionVerdict_SignalCombinations(t *testing.T) {
	const (
		p99         = int64(600000)
		elapsedSlow = int64(800000)
		elapsedOK   = int64(650000)
		sampled     = int64(600000)
		minSamp     = int64(20000)
		satMost     = int64(360000)
		satLittle   = int64(60000)
		goodN       = contentionMinSamples
	)
	tests := []struct {
		name        string
		elapsed     int64
		p99         int64
		sampleCount int
		sampledMS   int64
		saturatedMS int64
		wantFlag    bool
	}{
		{"all signals present flags", elapsedSlow, p99, goodN, sampled, satMost, true},
		{"not slow enough clears", elapsedOK, p99, goodN, sampled, satMost, false},
		{"host not saturated clears", elapsedSlow, p99, goodN, sampled, satLittle, false},
		{"too few samples never flags", elapsedSlow, p99, goodN - 1, sampled, satMost, false},
		{"no p99 baseline never flags", elapsedSlow, 0, goodN, sampled, satMost, false},
		{"too little observed time clears", elapsedSlow, p99, goodN, minSamp / 2, minSamp / 2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := contentionVerdict(tc.elapsed, tc.p99, tc.sampleCount, tc.sampledMS, tc.saturatedMS, minSamp)
			if ok != tc.wantFlag {
				t.Fatalf("contentionVerdict flag = %v, want %v (reason %q)", ok, tc.wantFlag, reason)
			}
			if ok && !strings.Contains(reason, "host saturated") {
				t.Errorf("flagged reason missing saturation share: %q", reason)
			}
			if ok && !strings.Contains(reason, "p99") {
				t.Errorf("flagged reason missing p99 baseline: %q", reason)
			}
		})
	}
}

func TestContentionVerdict_ReasonSharePercent(t *testing.T) {
	reason, ok := contentionVerdict(800000, 600000, 5, 600000, 360000, 20000)
	if !ok {
		t.Fatal("expected a contended verdict")
	}
	if !strings.Contains(reason, "60% of the run") {
		t.Errorf("reason = %q, want it to report a 60%% saturation share", reason)
	}
}
