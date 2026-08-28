package fs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// LogStore writes per-node logs as line-delimited JSON files under
// Root/<runID>/<nodeID>.ndjson. Append uses O_APPEND so concurrent
// writers from different processes interleave at line granularity
// (POSIX guarantees writes < PIPE_BUF are atomic).
type LogStore struct {
	Root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewLogStore returns a LogStore rooted at root, creating the
// directory if needed.
func NewLogStore(root string) (*LogStore, error) {
	if root == "" {
		return nil, errors.New("fs.NewLogStore: root required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &LogStore{
		Root:  root,
		locks: map[string]*sync.Mutex{},
	}, nil
}

var _ storage.LogStore = (*LogStore)(nil)

func (s *LogStore) lockFor(runID, nodeID string) *sync.Mutex {
	key := runID + "\x00" + nodeID
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	return m
}

func (s *LogStore) nodePath(runID, nodeID string) string {
	return filepath.Join(s.Root, runID, nodeID+".ndjson")
}

// RunDir is the directory this store writes runID's node logs into.
// The directory is named, not created: Append creates it on its first
// write, and a caller that wants it to exist before then says so with
// its own MkdirAll.
//
// Exported because the orchestrator records it as a run's `log_path`,
// so an operator holding a run id can read the logs straight off disk.
// It is a method on the concrete filesystem store rather than on
// storage.LogStore on purpose: "which local directory holds this run's
// logs" has no answer for the object-store, controller, or stdout
// implementations, and forcing them to return "" would make "no local
// directory" indistinguishable from "not implemented".
func (s *LogStore) RunDir(runID string) string {
	return filepath.Join(s.Root, runID)
}

func (s *LogStore) Append(_ context.Context, runID, nodeID string, data []byte) error {
	if err := storage.SafeLogIDs(runID, nodeID); err != nil {
		return fmt.Errorf("fs.LogStore.Append: %w", err)
	}
	m := s.lockFor(runID, nodeID)
	m.Lock()
	defer m.Unlock()

	path := s.nodePath(runID, nodeID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return nil
}

func (s *LogStore) Read(_ context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	if err := storage.SafeLogIDs(runID, nodeID); err != nil {
		return nil, fmt.Errorf("fs.LogStore.Read: %w", err)
	}
	data, err := os.ReadFile(s.nodePath(runID, nodeID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return applyReadOpts(data, opts)
}

func (s *LogStore) ReadRun(_ context.Context, runID string) ([]byte, error) {
	if err := storage.SafeSegment(runID); err != nil {
		return nil, fmt.Errorf("fs.LogStore.ReadRun: %w", err)
	}
	dir := s.RunDir(runID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var buf bytes.Buffer
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		nodeID := strings.TrimSuffix(e.Name(), ".ndjson")
		fmt.Fprintf(&buf, "=== %s ===\n", nodeID)
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		buf.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

// Stream is unsupported for the filesystem backend; callers fall
// back to polling Read.
func (s *LogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func (s *LogStore) DeleteRun(_ context.Context, runID string) error {
	if err := storage.SafeSegment(runID); err != nil {
		return fmt.Errorf("fs.LogStore.DeleteRun: %w", err)
	}
	err := os.RemoveAll(s.RunDir(runID))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func applyReadOpts(data []byte, opts storage.ReadOpts) ([]byte, error) {
	if (opts == storage.ReadOpts{}) {
		return data, nil
	}
	lines := splitLines(data)

	if opts.Lines != "" {
		from, to, err := parseRange(opts.Lines)
		if err != nil {
			return nil, err
		}
		if from < 1 {
			from = 1
		}
		if to > len(lines) {
			to = len(lines)
		}
		if from > len(lines) || from > to {
			lines = nil
		} else {
			lines = lines[from-1 : to]
		}
	}

	if opts.Grep != "" {
		filtered := lines[:0]
		for _, l := range lines {
			if strings.Contains(l, opts.Grep) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	if opts.Tail > 0 && len(lines) > opts.Tail {
		lines = lines[len(lines)-opts.Tail:]
	}
	if opts.Head > 0 && len(lines) > opts.Head {
		lines = lines[:opts.Head]
	}

	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	for _, l := range lines {
		_, _ = w.WriteString(l)
		_ = w.WriteByte('\n')
	}
	_ = w.Flush()
	return out.Bytes(), nil
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	if s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

func parseRange(spec string) (from, to int, err error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid lines range %q", spec)
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &from); err != nil {
		return 0, 0, fmt.Errorf("invalid lines.from %q: %w", parts[0], err)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &to); err != nil {
		return 0, 0, fmt.Errorf("invalid lines.to %q: %w", parts[1], err)
	}
	return from, to, nil
}
