package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

const (
	daemonFile     = "events.jsonl"
	supervisorFile = "supervisor-events.jsonl"
	maxRecordBytes = 1 << 20
	closeDeadline  = 100 * time.Millisecond
)

var lastIncarnation atomic.Uint64

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

// Writer accepts bounded, nonblocking event submissions and attempts a bounded drain on Close.
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
	r.TS = time.Now().UTC()
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

// Close gives accepted records a short flush deadline and reports abandoned records.
func (w *Writer) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		if w.dropped > 0 {
			w.seq++
			report := Record{TS: time.Now().UTC(), Seq: w.seq, Incarnation: w.incarnation, Source: "daemon", Kind: "dropped", Data: map[string]any{"count": w.dropped}}
			select {
			case w.ch <- report:
				w.dropped = 0
			default:
				select {
				case <-w.ch:
					w.dropped++
				default:
				}
				report.Data["count"] = w.dropped
				select {
				case w.ch <- report:
					w.dropped = 0
				default:
				}
			}
		}
		close(w.ch)
	}
	w.mu.Unlock()
	timer := time.NewTimer(closeDeadline)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
		w.mu.Lock()
		var abandoned uint64
		// safety: the writer may still finish its current write, so only queued records count as abandoned.
		for r := range w.ch {
			if r.Kind == "dropped" {
				if count, ok := r.Data["count"].(uint64); ok {
					abandoned += count
					continue
				}
			}
			abandoned++
		}
		w.dropped += abandoned
		w.mu.Unlock()
		if abandoned > 0 && w.logf != nil {
			go w.logf("journal: close deadline dropped %d queued records", abandoned)
		}
	}
}

func (w *Writer) run() {
	defer close(w.done)
	a := &appender{dir: w.dir, name: daemonFile, maxSize: 16 << 20, files: 3}
	defer func() {
		if err := a.close(); err != nil && w.logf != nil {
			w.logf("journal: %v", err)
		}
	}()
	for r := range w.ch {
		if err := a.append(r); err != nil && w.logf != nil {
			w.logf("journal: %v", err)
		}
	}
}

// AppendSupervisor writes a supervisor record to a separate stream in the same directory.
func AppendSupervisor(dir string, r Record) error {
	if r.TS.IsZero() {
		r.TS = time.Now().UTC()
	}
	r.Source = "supervisor"
	return appendRecord(dir, supervisorFile, 1<<20, 2, r)
}

func appendRecord(dir, name string, maxSize int64, files int, r Record) error {
	a := &appender{dir: dir, name: name, maxSize: maxSize, files: files}
	err := a.append(r)
	return errors.Join(err, a.close())
}

type appender struct {
	dir     string
	name    string
	maxSize int64
	files   int
	f       *os.File
	size    int64
}

func (a *appender) append(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(line) > maxRecordBytes {
		return fmt.Errorf("journal record exceeds 1 MiB")
	}
	if int64(len(line)+1) > a.maxSize {
		return fmt.Errorf("journal record exceeds file cap")
	}
	if a.f == nil {
		if err := a.open(); err != nil {
			return err
		}
	}
	if a.size+int64(len(line)+1) > a.maxSize {
		if err := a.rotate(); err != nil {
			return err
		}
		if err := a.open(); err != nil {
			return err
		}
	}
	n, err := a.f.Write(append(line, '\n'))
	a.size += int64(n)
	if err != nil || n != len(line)+1 {
		if err == nil {
			err = fmt.Errorf("short journal write")
		}
		err = errors.Join(err, a.close())
	}
	return err
}

func (a *appender) open() error {
	if err := fssecure.EnsureDir(a.dir); err != nil {
		return err
	}
	path := filepath.Join(a.dir, a.name)
	fi, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		a.size = fi.Size()
		if fi.Size() > 0 {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			last := make([]byte, 1)
			_, err = f.ReadAt(last, fi.Size()-1)
			err = errors.Join(err, f.Close())
			if err != nil {
				return err
			}
			if fi.Size() > a.maxSize {
				if err := os.Remove(path); err != nil {
					return err
				}
				a.size = 0
			} else if last[0] != '\n' {
				if err := a.rotate(); err != nil {
					return err
				}
			}
		}
	}
	a.f, err = fssecure.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	return err
}

func (a *appender) rotate() error {
	if err := a.close(); err != nil {
		return err
	}
	path := filepath.Join(a.dir, a.name)
	if err := os.Remove(fmt.Sprintf("%s.%d", path, a.files-1)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := a.files - 1; i >= 1; i-- {
		old := path
		if i > 1 {
			old = fmt.Sprintf("%s.%d", path, i-1)
		}
		if err := os.Rename(old, fmt.Sprintf("%s.%d", path, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	a.size = 0
	return nil
}

func (a *appender) close() error {
	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	return err
}

// Read returns all retained records, including records written by the supervisor.
func Read(dir string) ([]Record, error) {
	records, _, err := ReadWithStats(dir)
	return records, err
}

// ReadWithStats returns retained records and the number of malformed or oversized lines skipped.
func ReadWithStats(dir string) ([]Record, int, error) {
	var records []Record
	var skipped int
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
				return nil, skipped, err
			}
			lines := bytes.Split(body, []byte{'\n'})
			for lineIndex, line := range lines {
				last := lineIndex == len(lines)-1
				if last && len(line) == 0 {
					continue
				}
				if len(line) > maxRecordBytes {
					skipped++
					continue
				}
				var r Record
				if err := json.Unmarshal(line, &r); err != nil {
					skipped++
					continue
				}
				records = append(records, r)
			}
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if !a.TS.Equal(b.TS) {
			return a.TS.Before(b.TS)
		}
		if a.Incarnation != b.Incarnation {
			return a.Incarnation < b.Incarnation
		}
		return a.Seq < b.Seq
	})
	return records, skipped, nil
}

// NextIncarnation chooses a daemon identity without waiting for journal storage.
func NextIncarnation() uint64 {
	for {
		n := uint64(time.Now().UnixNano())
		previous := lastIncarnation.Load()
		if n <= previous {
			n = previous + 1
		}
		if lastIncarnation.CompareAndSwap(previous, n) {
			return n
		}
	}
}

// PersistIncarnation records the next identity and returns the value written.
func PersistIncarnation(dir string, n uint64) (selected uint64, result error) {
	path := filepath.Join(dir, "incarnation")
	var readErr error
	if b, err := os.ReadFile(path); err == nil {
		if previous, err := parseIncarnation(b); err == nil {
			if previous == ^uint64(0) {
				return 0, fmt.Errorf("incarnation counter exhausted")
			}
			n = previous + 1
		} else {
			readErr = fmt.Errorf("invalid incarnation %q", strings.TrimSpace(string(b)))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		readErr = fmt.Errorf("read incarnation: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "incarnation-*.tmp")
	if err != nil {
		return n, errors.Join(readErr, fmt.Errorf("create incarnation temp: %w", err))
	}
	tmpPath := tmp.Name()
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("remove incarnation temp: %w", err))
		}
	}()
	_, writeErr := tmp.Write([]byte(fmt.Sprint(n)))
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return n, errors.Join(readErr, fmt.Errorf("write incarnation: %w", err))
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return n, errors.Join(readErr, fmt.Errorf("replace incarnation: %w", err))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return n, errors.Join(readErr, fmt.Errorf("list incarnation temps: %w", err))
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "incarnation-") && strings.HasSuffix(entry.Name(), ".tmp") {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove incarnation temp: %w", err))
			}
		}
	}
	return n, errors.Join(readErr, result)
}

// Incarnation reads the latest elected daemon number.
func Incarnation(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "incarnation"))
	if err != nil {
		return 0
	}
	n, err := parseIncarnation(b)
	if err != nil {
		return 0
	}
	return n
}

func parseIncarnation(b []byte) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
