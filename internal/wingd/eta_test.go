package wingd

import (
	"math"
	"testing"
)

func TestSimulateQueue_ETA(t *testing.T) {
	inf := math.Inf(1)
	cases := []struct {
		name       string
		capCores   float64
		holders    []simRun
		waiters    []simRun
		wantStarts []float64
		wantClear  float64
	}{
		{
			name:       "both fit immediately",
			capCores:   8,
			waiters:    []simRun{{cores: 4, duration: 10000}, {cores: 4, duration: 10000}},
			wantStarts: []float64{0, 0},
			wantClear:  10000,
		},
		{
			name:       "second serializes behind first",
			capCores:   8,
			waiters:    []simRun{{cores: 8, duration: 5000}, {cores: 8, duration: 3000}},
			wantStarts: []float64{0, 5000},
			wantClear:  8000,
		},
		{
			name:       "waiter waits for a holder to free capacity",
			capCores:   8,
			holders:    []simRun{{cores: 8, finish: 4000}},
			waiters:    []simRun{{cores: 8, duration: 2000}},
			wantStarts: []float64{4000},
			wantClear:  6000,
		},
		{
			name:       "unknown holder duration blocks ETA",
			capCores:   8,
			holders:    []simRun{{cores: 8, finish: inf}},
			waiters:    []simRun{{cores: 8, duration: 2000}},
			wantStarts: []float64{inf},
			wantClear:  inf,
		},
		{
			name:       "waiter that fits gets a start even with unknown duration",
			capCores:   8,
			waiters:    []simRun{{cores: 4, duration: inf}},
			wantStarts: []float64{0},
			wantClear:  inf,
		},
		{
			name:       "millicore rounding at capacity still fits",
			capCores:   7.889999999,
			waiters:    []simRun{{cores: 7.89, duration: 5000}},
			wantStarts: []float64{0},
			wantClear:  5000,
		},
		{
			name:       "oversized waiter makes clear time unknown",
			capCores:   8,
			waiters:    []simRun{{cores: 9, duration: 5000}},
			wantStarts: []float64{inf},
			wantClear:  inf,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			starts, clear := simulateQueue(tc.capCores, 1<<50, tc.holders, tc.waiters)
			if len(starts) != len(tc.wantStarts) {
				t.Fatalf("starts len = %d, want %d", len(starts), len(tc.wantStarts))
			}
			for i := range starts {
				if !approxEqInf(starts[i], tc.wantStarts[i]) {
					t.Errorf("start[%d] = %v, want %v", i, starts[i], tc.wantStarts[i])
				}
			}
			if !approxEqInf(clear, tc.wantClear) {
				t.Errorf("clear = %v, want %v", clear, tc.wantClear)
			}
		})
	}
}

func approxEqInf(a, b float64) bool {
	if math.IsInf(a, 1) || math.IsInf(b, 1) {
		return math.IsInf(a, 1) && math.IsInf(b, 1)
	}
	return math.Abs(a-b) < 1e-6
}
