package nodemetrics

import (
	"context"
	"testing"
	"time"
)

func TestAttachPreservesUnavailableReadings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cpu   []time.Duration
		cpuOK []bool
		rss   []int64
		rssOK []bool
		valid []bool
	}{
		{"zero", []time.Duration{0, 0}, []bool{true, true}, []int64{0}, []bool{true}, []bool{true}},
		{"initial failure", []time.Duration{0, 1, 2}, []bool{false, true, true}, []int64{100, 100}, []bool{true, true}, []bool{false, true}},
		{"interruption", []time.Duration{0, 1, 2, 3, 4}, []bool{true, true, false, true, true}, []int64{100, 100, 100, 100}, []bool{true, true, true, true}, []bool{true, false, false, true}},
		{"counter reset", []time.Duration{2, 1, 2}, []bool{true, true, true}, []int64{100, 100}, []bool{true, true}, []bool{false, true}},
		{"negative baseline", []time.Duration{-1, 0, 1}, []bool{true, true, true}, []int64{100, 100}, []bool{true, true}, []bool{false, true}},
		{"negative read", []time.Duration{0, -1, 0, 1}, []bool{true, true, true, true}, []int64{100, 100, 100}, []bool{true, true, true}, []bool{false, false, true}},
		{"RSS failure", []time.Duration{0, 1, 2}, []bool{true, true, true}, []int64{999, 100}, []bool{false, true}, []bool{false, true}},
		{"negative RSS", []time.Duration{0, 1, 2}, []bool{true, true, true}, []int64{-1, 100}, []bool{true, true}, []bool{false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(SetIntervalForTest(5 * time.Millisecond))
			oldCPU, oldRSS := cpuReader, rssReader
			defer func() { cpuReader, rssReader = oldCPU, oldRSS }()
			c, r := 0, 0
			cpuReader = func() (time.Duration, bool) {
				i := min(c, len(tc.cpu)-1)
				c++
				return tc.cpu[i] * time.Second, tc.cpuOK[i]
			}
			rssReader = func() (int64, bool) { i := min(r, len(tc.rss)-1); r++; return tc.rss[i], tc.rssOK[i] }
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			sink := &captureSink{sampleReady: make(chan struct{}, 16)}
			detach := Attach(ctx, sink)
			defer func() { detach(); waitForSamplerStop(t) }()
			for i, want := range tc.valid {
				select {
				case <-sink.sampleReady:
				case <-ctx.Done():
					t.Fatal("sample unavailable before test deadline")
				}
				got := sink.snapshot()[i]
				if got.Valid != want {
					t.Errorf("sample %d valid=%t, want %t", i, got.Valid, want)
				}
				memory := tc.rss[i]
				if !tc.rssOK[i] || memory < 0 {
					memory = 0
				}
				if got.MemoryBytes != memory {
					t.Errorf("sample %d RSS=%d, want %d", i, got.MemoryBytes, memory)
				}
				cpuValid := tc.cpuOK[i] && tc.cpuOK[i+1] && tc.cpu[i] >= 0 && tc.cpu[i+1] >= tc.cpu[i]
				positive := cpuValid && tc.cpu[i+1] > tc.cpu[i]
				if (got.CPUMillicores > 0) != positive {
					t.Errorf("sample %d CPU=%d, want positive=%t", i, got.CPUMillicores, positive)
				}
			}
		})
	}
}
