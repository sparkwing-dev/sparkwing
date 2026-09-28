package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

const (
	daemonFile     = "events.jsonl"
	supervisorFile = "supervisor-events.jsonl"
	maxRecordBytes = 1 << 20
)

// Record describes one decision or observation made by the daemon or its supervisor.
type Record struct {
	TS           time.Time      `json:"ts"`
	Seq          uint64         `json:"seq"`
	Incarnation  uint64         `json:"incarnation"`
	Source       string         `json:"source"`
	Kind         string         `json:"kind"`
	RunID        string         `json:"run_id,omitempty"`
	DisplayRunID string         `json:"display_run_id,omitempty"`
	Pipeline     string         `json:"pipeline,omitempty"`
	Repo         string         `json:"repo,omitempty"`
	PID          int            `json:"pid,omitempty"`
	Data         map[string]any `json:"data,omitempty"`
}

// Writer accepts bounded, nonblocking event submissions and drains them on Close.
type Writer struct {
	ch          chan Record
	done        chan struct{}
	mu          sync.Mutex
	dropped     uint64
	seq         uint64
	incarnation uint64
	dir         string
	logf        func(string, ...any)
	closed      bool
}

// NewWriter starts the daemon's private journal writer.
func NewWriter(dir string, incarnation uint64, logf func(string, ...any)) *Writer {
	w := &Writer{ch: make(chan Record, 1024), done: make(chan struct{}), dir: dir, incarnation: incarnation, logf: logf}
	go w.run()
	return w
}

// Enqueue never touches disk and may drop an event when the buffer is full.
func (w *Writer) Enqueue(r Record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if r.TS.IsZero() {
		r.TS = time.Now().UTC()
	}
	if w.dropped > 0 && len(w.ch) < cap(w.ch)-1 {
		w.seq++
		w.ch <- Record{TS: r.TS, Seq: w.seq, Incarnation: w.incarnation, Source: "daemon", Kind: "dropped", Data: map[string]any{"count": w.dropped}}
		w.dropped = 0
	}
	w.seq++
	r.Seq, r.Incarnation, r.Source = w.seq, w.incarnation, "daemon"
	select {
	case w.ch <- r:
	default:
		w.dropped++
	}
}

// Close flushes accepted records and reports any final overflow.
func (w *Writer) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.done
		return
	}
	w.closed = true
	if w.dropped > 0 {
		w.seq++
		w.ch <- Record{TS: time.Now().UTC(), Seq: w.seq, Incarnation: w.incarnation, Source: "daemon", Kind: "dropped", Data: map[string]any{"count": w.dropped}}
		w.dropped = 0
	}
	close(w.ch)
	w.mu.Unlock()
	<-w.done
}

func (w *Writer) run() {
	defer close(w.done)
	for r := range w.ch {
		if err := appendRecord(w.dir, daemonFile, 16<<20, 3, r); err != nil && w.logf != nil {
			w.logf("journal: %v", err)
		}
	}
}

// AppendSupervisor writes a supervisor record to a separate stream in the same directory.
func AppendSupervisor(dir string, r Record) error {
	r.TS, r.Source = time.Now().UTC(), "supervisor"
	return appendRecord(dir, supervisorFile, 1<<20, 2, r)
}

func appendRecord(dir, name string, maxSize int64, files int, r Record) error {
	if err := fssecure.EnsureDir(dir); err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(line) > maxRecordBytes {
		return fmt.Errorf("journal record exceeds 1 MiB")
	}
	if int64(len(line)+1) > maxSize {
		return fmt.Errorf("journal record exceeds file cap")
	}
	path := filepath.Join(dir, name)
	fi, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rotate := false
	if err == nil {
		rotate = fi.Size()+int64(len(line)+1) > maxSize
		if fi.Size() > 0 {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			last := make([]byte, 1)
			_, err = f.ReadAt(last, fi.Size()-1)
			_ = f.Close()
			if err != nil {
				return err
			}
			rotate = rotate || last[0] != '\n'
		}
	}
	if rotate {
		if fi.Size() > maxSize {
			if err := os.Remove(path); err != nil {
				return err
			}
			rotate = false
		}
	}
	if rotate {
		if err := os.Remove(fmt.Sprintf("%s.%d", path, files-1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for i := files - 1; i >= 1; i-- {
			old := path
			if i > 1 {
				old = fmt.Sprintf("%s.%d", path, i-1)
			}
			if err := os.Rename(old, fmt.Sprintf("%s.%d", path, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	f, err := fssecure.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Read returns all retained records, including records written by the supervisor.
func Read(dir string) ([]Record, error) {
	var records []Record
	for _, stream := range []struct {
		name  string
		files int
	}{{daemonFile, 3}, {supervisorFile, 2}} {
		for i := stream.files - 1; i >= 0; i-- {
			path := filepath.Join(dir, stream.name)
			if i > 0 {
				path = fmt.Sprintf("%s.%d", path, i)
			}
			body, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			lines := bytes.Split(body, []byte{'\n'})
			for lineIndex, line := range lines {
				last := lineIndex == len(lines)-1
				if last && len(line) == 0 {
					continue
				}
				if len(line) > maxRecordBytes {
					return nil, fmt.Errorf("%s: oversized journal record", path)
				}
				var r Record
				if err := json.Unmarshal(line, &r); err != nil {
					if last && body[len(body)-1] != '\n' {
						continue
					}
					return nil, fmt.Errorf("%s: %w", path, err)
				}
				records = append(records, r)
			}
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].TS.Before(records[j].TS) })
	return records, nil
}

// NextIncarnation advances the counter after daemon election has acquired its lock.
func NextIncarnation(dir string) (uint64, error) {
	path := filepath.Join(dir, "incarnation")
	var n uint64
	if b, err := os.ReadFile(path); err == nil {
		if _, err := fmt.Sscan(string(b), &n); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	n++
	tmp := path + ".tmp"
	if err := fssecure.WriteFile(tmp, []byte(fmt.Sprint(n))); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return n, nil
}

// Incarnation reads the latest elected daemon number.
func Incarnation(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "incarnation"))
	if err != nil {
		return 0
	}
	var n uint64
	if _, err := fmt.Sscan(string(b), &n); err != nil {
		return 0
	}
	return n
}
