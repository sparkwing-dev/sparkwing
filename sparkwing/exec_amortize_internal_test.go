package sparkwing

import (
	"testing"
	"time"
)

// TestAmortizedMillicores checks that a finished command's CPU is spread over
// its own wall duration, so a long parallel child reads as its real
// concurrency rather than a reap-time burst. A 2s 8-way child (16 CPU-seconds)
// records ~8 cores, and a 5s child drawing 40 CPU-seconds records the same 8,
// never the 20-plus a single sampler interval would show.
func TestAmortizedMillicores(t *testing.T) {
	cases := []struct {
		name string
		cpu  time.Duration
		wall time.Duration
		want int64
	}{
		{"8-way child over 2s wall", 16 * time.Second, 2 * time.Second, 8000},
		{"make -j8 over 5s wall", 40 * time.Second, 5 * time.Second, 8000},
		{"single core", 2 * time.Second, 2 * time.Second, 1000},
		{"zero wall draws nothing", time.Second, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := amortizedMillicores(tc.cpu, tc.wall); got != tc.want {
				t.Errorf("amortizedMillicores(%v, %v) = %d, want %d", tc.cpu, tc.wall, got, tc.want)
			}
		})
	}
}
