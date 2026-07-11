package chaos

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Event is one line in the chaos journal: a timestamped, sequenced record
// of everything the harness did or observed. A failing run is replayed by
// re-running with the printed seed; the journal is the human-readable
// trace of what that seed produced.
type Event struct {
	Seq    int64          `json:"seq"`
	AtMS   int64          `json:"at_ms"`
	Kind   string         `json:"kind"`
	Run    string         `json:"run,omitempty"`
	Detail string         `json:"detail,omitempty"`
	Key    string         `json:"key,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Journal is an append-only JSONL sink for chaos events. It is safe for
// concurrent use; every injector and oracle writes through it so the seed
// plus the journal fully explain a failure.
type Journal struct {
	mu    sync.Mutex
	f     *os.File
	enc   *json.Encoder
	start time.Time
	seq   int64
	path  string
}

// NewJournal creates a journal file at path.
func NewJournal(path string) (*Journal, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Journal{f: f, enc: json.NewEncoder(f), start: time.Now(), path: path}, nil
}

// Path returns the journal file path, printed prominently on failure.
func (j *Journal) Path() string { return j.path }

// Append writes one event, stamping it with a sequence number and the
// milliseconds elapsed since the journal was created.
func (j *Journal) Append(e Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	e.Seq = j.seq
	e.AtMS = time.Since(j.start).Milliseconds()
	_ = j.enc.Encode(&e)
}

// Log is a convenience for a bare kind-with-detail event.
func (j *Journal) Log(kind, run, detail string) {
	j.Append(Event{Kind: kind, Run: run, Detail: detail})
}

// Close flushes and closes the underlying file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}
