package chaos

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type Event struct {
	Seq    int64          `json:"seq"`
	AtMS   int64          `json:"at_ms"`
	Kind   string         `json:"kind"`
	Run    string         `json:"run,omitempty"`
	Detail string         `json:"detail,omitempty"`
	Key    string         `json:"key,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

type Journal struct {
	mu    sync.Mutex
	f     *os.File
	enc   *json.Encoder
	start time.Time
	seq   int64
	path  string
}

func NewJournal(path string) (*Journal, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Journal{f: f, enc: json.NewEncoder(f), start: time.Now(), path: path}, nil
}

func (j *Journal) Path() string { return j.path }

func (j *Journal) Append(e Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	e.Seq = j.seq
	e.AtMS = time.Since(j.start).Milliseconds()
	_ = j.enc.Encode(&e)
}

func (j *Journal) Log(kind, run, detail string) {
	j.Append(Event{Kind: kind, Run: run, Detail: detail})
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}
