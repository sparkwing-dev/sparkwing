package chaos

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type event struct {
	Seq    int64          `json:"seq"`
	AtMS   int64          `json:"at_ms"`
	Kind   string         `json:"kind"`
	Run    string         `json:"run,omitempty"`
	Detail string         `json:"detail,omitempty"`
	Key    string         `json:"key,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

type journal struct {
	mu    sync.Mutex
	f     *os.File
	enc   *json.Encoder
	start time.Time
	seq   int64
	path  string
}

func newJournal(path string) (*journal, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &journal{f: f, enc: json.NewEncoder(f), start: time.Now(), path: path}, nil
}

func (j *journal) filename() string { return j.path }

func (j *journal) append(e event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	e.Seq = j.seq
	e.AtMS = time.Since(j.start).Milliseconds()
	_ = j.enc.Encode(&e)
}

func (j *journal) log(kind, run, detail string) {
	j.append(event{Kind: kind, Run: run, Detail: detail})
}

func (j *journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}
