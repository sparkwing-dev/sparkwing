package nodemetrics

import (
	"testing"
	"time"
)

func TestIntervalSampleAvailabilityAndBounds(t *testing.T) {
	start := time.Unix(100, 0)
	before := reading{start, time.Second, 100, true, nil}
	after := reading{start.Add(2 * time.Second), 2 * time.Second, 200, true, nil}
	for _, tc := range []struct {
		name        string
		change      func(*reading, *reading)
		valid       bool
		cpu, memory int64
	}{
		{"measured", func(*reading, *reading) {}, true, 500, 200},
		{"measured zero", func(a, b *reading) { b.cpu = a.cpu; b.memory = 0 }, true, 0, 0},
		{"missing baseline", func(a, b *reading) { a.valid = false }, false, 0, 200},
		{"missing reading", func(a, b *reading) { b.valid = false }, false, 0, 0},
		{"counter reset", func(a, b *reading) { b.cpu = 0 }, false, 0, 200},
		{"negative baseline", func(a, b *reading) { a.cpu = -1 }, false, 0, 200},
		{"negative CPU", func(a, b *reading) { b.cpu = -1 }, false, 0, 200},
		{"negative memory", func(a, b *reading) { b.memory = -1 }, false, 0, 0},
		{"invalid baseline memory", func(a, b *reading) { a.memory = -1 }, false, 0, 200},
		{"zero duration", func(a, b *reading) { b.at = a.at }, false, 0, 200},
		{"backward time", func(a, b *reading) { b.at = a.at.Add(-time.Second) }, false, 0, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := before, after
			tc.change(&a, &b)
			got := intervalSample(a, b)
			if got.Valid != tc.valid || got.CPUMillicores != tc.cpu || got.MemoryBytes != tc.memory || !got.TS.Equal(b.at) {
				t.Fatalf("sample=%+v; want valid=%t CPU=%d memory=%d at%s", got, tc.valid, tc.cpu, tc.memory, b.at)
			}
		})
	}
}
