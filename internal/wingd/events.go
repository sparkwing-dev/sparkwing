package wingd

import (
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const eventWindowSpan = 24 * time.Hour

const eventWindowCap = 4096

type admissionEvent struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	WaitMS int64     `json:"wait_ms,omitempty"`
	Key    string    `json:"key,omitempty"`

	BackfillCount uint64 `json:"backfill_count,omitempty"`
}

const (
	eventGrant        = "grant"
	eventEviction     = "eviction"
	eventQueueTimeout = "queue_timeout"
	eventCancellation = "cancellation"
	eventContended    = "contended"
	eventRejection    = "rejection"
	eventBackfill     = "backfill"
)

type eventWindow struct {
	mu      sync.Mutex
	entries []admissionEvent
}

func (w *eventWindow) record(now time.Time, ev admissionEvent) {
	ev.At = now
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, ev)
	w.pruneLocked(now)
}

func (w *eventWindow) restore(now time.Time, entries []admissionEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append([]admissionEvent(nil), entries...)
	w.pruneLocked(now)
}

func (w *eventWindow) pruneLocked(now time.Time) {
	cutoff := now.Add(-eventWindowSpan)
	first := 0
	for first < len(w.entries) && w.entries[first].At.Before(cutoff) {
		first++
	}
	if over := len(w.entries) - first - eventWindowCap; over > 0 {
		first += over
	}
	if first > 0 {
		w.entries = append([]admissionEvent(nil), w.entries[first:]...)
	}
}

func (w *eventWindow) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = nil
}

func (w *eventWindow) snapshot(now time.Time) []admissionEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked(now)
	return append([]admissionEvent(nil), w.entries...)
}

func (w *eventWindow) summary(now time.Time) *wingwire.EventsWindow {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked(now)
	if len(w.entries) == 0 {
		return nil
	}
	out := &wingwire.EventsWindow{WindowMS: eventWindowSpan.Milliseconds()}
	var waits []int64
	evictions := map[string]int{}
	rejections := map[string]int{}
	for _, e := range w.entries {
		switch e.Kind {
		case eventGrant:
			out.Runs++
			waits = append(waits, e.WaitMS)
		case eventEviction:
			evictions[e.Key]++
		case eventQueueTimeout:
			out.QueueTimeouts++
		case eventCancellation:
			out.Cancellations++
		case eventContended:
			out.Contended++
		case eventRejection:
			rejections[e.Key]++
		case eventBackfill:
			out.Backfills++
			if e.BackfillCount == 1 {
				out.BackfillProtections++
			}
		}
	}
	if len(waits) > 0 {
		sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
		out.MedianWaitMS = waits[(len(waits)-1)/2]
	}
	keys := make([]string, 0, len(evictions))
	for k := range evictions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Evictions = append(out.Evictions, wingwire.EvictionCount{Key: k, Count: evictions[k]})
	}
	causes := make([]string, 0, len(rejections))
	for k := range rejections {
		causes = append(causes, k)
	}
	sort.Strings(causes)
	for _, k := range causes {
		out.Rejections = append(out.Rejections, wingwire.RejectionCount{Cause: k, Count: rejections[k]})
	}
	return out
}
