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
			oldCPU, oldRSS := cpuReader, rssReader
			defer func() { cpuReader, rssReader = oldCPU, oldRSS }()
			read := 0
			cpuReader = func() (time.Duration, bool) {
				i := read
				read++
				return time.Duration(i) * 5 * time.Millisecond, tc.available[min(i, len(tc.available)-1)]
			}
			rssReader = func() int64 { return 1024 }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sink := &captureSink{sampleReady: make(chan struct{}, 16)}
			detach := Attach(ctx, sink)
			defer func() { detach(); waitForSamplerStop(t) }()
			for i, positive := range tc.wantPositive {
				select {
				case <-sink.sampleReady:
				case <-ctx.Done():
					t.Fatal("sampler failed to emit expected readings")
				}
				sink.mu.Lock()
				got := sink.samples[i].CPUMillicores
				sink.mu.Unlock()
				if (got > 0) != positive {
					t.Errorf("reading %d: CPU = %d millicores; positive = %t, want %t", i, got, got > 0, positive)
				}
			}
		})
	}
}

func TestAttach_TimestampsCompletedCPURead(t *testing.T) {
	t.Cleanup(SetIntervalForTest(5 * time.Millisecond))
	oldCPU, oldRSS := cpuReader, rssReader
	defer func() { cpuReader, rssReader = oldCPU, oldRSS }()
	completed := make(chan time.Time, 16)
	cpuReader = func() (time.Duration, bool) {
		select {
		case completed <- time.Now():
		default:
		}
		return 0, true
	}
	rssReader = func() int64 { return 1024 }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sink := &captureSink{sampleReady: make(chan struct{}, 16)}
	detach := Attach(ctx, sink)
	defer func() { detach(); waitForSamplerStop(t) }()
	select {
	case <-sink.sampleReady:
	case <-ctx.Done():
		t.Fatal("sampler did not emit a reading")
	}
	<-completed
	readAt := <-completed
	sink.mu.Lock()
	sample := sink.samples[0]
	sink.mu.Unlock()
	if sample.TS.Before(readAt) {
		t.Fatalf("sample timestamp %s precedes CPU read %s", sample.TS, readAt)
	}
}
