package nodemetrics

import (
	"context"
	"testing"
	"time"
)

func TestAttach_CPUReaderRecoversWithAFreshBaseline(t *testing.T) {
	for _, tc := range []struct {
		name         string
		available    []bool
		wantPositive []bool
	}{
		{"initial failure", []bool{false, true, true}, []bool{false, true}},
		{"interrupted readings", []bool{true, true, false, true, true}, []bool{true, false, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(SetIntervalForTest(5 * time.Millisecond))
			read := 0
			stubReaders(t, func() (time.Duration, bool) {
				i := read
				read++
				return time.Duration(i) * 5 * time.Millisecond, tc.available[min(i, len(tc.available)-1)]
			}, func() int64 { return 1024 })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sink := &captureSink{sampleReady: make(chan struct{}, 16)}
			detach := Attach(ctx, sink, false)
			defer func() { detach(); waitForSamplerStop(t) }()
			for i, positive := range tc.wantPositive {
				select {
				case <-sink.sampleReady:
				case <-ctx.Done():
					t.Fatal("sampler failed to emit expected readings")
				}
				sink.mu.Lock()
				got := sink.samples[i].CPUMillicores
				available := sink.samples[i].CPUAvailable
				sink.mu.Unlock()
				if available != positive {
					t.Errorf("reading %d: CPU availability=%t want %t", i, available, positive)
				}
				if (got > 0) != positive {
					t.Errorf("reading %d: CPU = %d millicores; positive = %t, want %t", i, got, got > 0, positive)
				}
			}
		})
	}
}
