package orchestrator

import (
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
)

// Parallel commands can finish on one clock reading, and the store refuses a
// second sample at a node's timestamp, so the sink must move the later one.
func TestStateMetricsSinkNeverRepeatsATimestamp(t *testing.T) {
	backend := &metricCaptureBackend{}
	stamps := &metricStamps{}
	sink := stateMetricsSink{backend: backend, runID: "run", nodeID: "node", stamps: stamps}
	at := time.Unix(100, 0)
	if err := sink.Push(t.Context(), nodemetrics.Sample{Valid: true, TS: at}); err != nil {
		t.Fatal(err)
	}
	first := backend.sample.TS
	if got := stamps.next(at); !got.After(first) {
		t.Fatalf("command stamp %s does not follow the interval sample at %s", got, first)
	}
	if err := sink.Push(t.Context(), nodemetrics.Sample{Valid: true, TS: at.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if !backend.sample.TS.After(first) {
		t.Fatalf("a clock that stepped back reused %s", backend.sample.TS)
	}
}

func TestMetricStampsNeverRepeatStoredTimestampsForConcurrentCommands(t *testing.T) {
	stamps := &metricStamps{}
	const commands = 8
	const samplesPerCommand = 10_000
	samples := make(chan int64, commands*samplesPerCommand)
	var writers sync.WaitGroup
	for range commands {
		writers.Go(func() {
			for range samplesPerCommand {
				samples <- stamps.next(time.Now()).UnixNano()
			}
		})
	}
	writers.Wait()
	close(samples)
	seen := make(map[int64]bool, commands*samplesPerCommand)
	for stamp := range samples {
		if seen[stamp] {
			t.Fatalf("concurrent commands reused stored timestamp %d", stamp)
		}
		seen[stamp] = true
	}
}
