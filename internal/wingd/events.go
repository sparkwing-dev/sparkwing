package wingd

import (
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// eventWindowSpan is how far back the daemon's admission-outcome window
// reaches; older entries are pruned on every append and summary.
const eventWindowSpan = 24 * time.Hour

// eventWindowCap bounds the window's entry count so a pathological burst
// cannot grow the persisted state without limit; the oldest entries are
// dropped first.
const eventWindowCap = 4096

// admissionEvent is one admission outcome in the daemon's rolling
// window. Exactly one Kind per entry; WaitMS is meaningful for grants
// and Key for evictions.
type admissionEvent struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	WaitMS int64     `json:"wait_ms,omitempty"`
	Key    string    `json:"key,omitempty"`
}

// Event kinds recorded in the window.
const (
	eventGrant        = "grant"
	eventEviction     = "eviction"
	eventQueueTimeout = "queue_timeout"
	eventCancellation = "cancellation"
)

// eventWindow is the daemon's bounded rolling record of admission
// outcomes. It has its own lock so the persistence path can snapshot it
// without holding the daemon mutex.
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

// restore seeds the window from persisted entries, dropping anything
// already outside the span.
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

// snapshot returns a copy of the live entries for persistence.
func (w *eventWindow) snapshot(now time.Time) []admissionEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked(now)
	return append([]admissionEvent(nil), w.entries...)
}

// summary folds the window into the wire form the queue view renders.
// Nil when the window is empty, so older-daemon and nothing-happened
// payloads look the same.
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
	return out
}
