// Package stdoutlogs implements a storage.LogStore that streams log
// payloads to the process's standard output instead of persisting
// them.
//
// Intended for ephemeral CI runs (GitHub Actions, kubectl-style log
// scrape) where the operator wants log lines flowing through the
// terminal and captured by whatever tool tails the process, without
// the overhead of an s3 / filesystem / controller log object store.
//
// The backend is stateless and write-only. Read, ReadRun, and Stream
// return an error: there is nothing to read back, and silently
// returning empty bytes would hide that fact from a dashboard caller.
// DeleteRun is a no-op (idempotent), since no per-run object exists
// to delete.
//
// Runs the shared pkg/storage/conformance.TestLogStore suite. The
// read-side subtests skip on ErrReadUnsupported (which wraps
// storage.ErrNotSupported).
package stdoutlogs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// LogStore writes log payloads to an io.Writer (os.Stdout in
// production). One mutex serializes every write so concurrent jobs
// don't interleave at byte boundaries and produce torn output.
type LogStore struct {
	mu sync.Mutex
	w  io.Writer
}

// New returns a LogStore that writes to os.Stdout.
func New() *LogStore {
	return NewWithWriter(os.Stdout)
}

// NewWithWriter routes writes to w instead of os.Stdout. Test-only;
// production callers use New().
func NewWithWriter(w io.Writer) *LogStore {
	return &LogStore{w: w}
}

var _ storage.LogStore = (*LogStore)(nil)

// ErrReadUnsupported is returned by every read path on this backend.
// Wraps [storage.ErrNotSupported] so callers using either the local
// or shared sentinel can detect the case via errors.Is.
var ErrReadUnsupported = fmt.Errorf("stdout logs backend does not support reads; configure a persistent backend (filesystem, s3, controller) for log retrieval: %w", storage.ErrNotSupported)

// Append writes the payload to stdout, prefixing each non-empty line
// with `<runID> <nodeID> | ` so concurrent jobs are disentangled by a
// terminal-side reader. The payload's bytes (including any embedded
// JSON, ANSI codes, or trailing newlines) are otherwise untouched.
//
// The prefix format is part of the public surface: external tooling
// can split on ` | ` to recover the original payload. Two
// whitespace-separated tokens precede the delimiter so the runID and
// nodeID are individually addressable.
func (s *LogStore) Append(_ context.Context, runID, nodeID string, data []byte) error {
	if runID == "" || nodeID == "" {
		return errors.New("stdoutlogs.LogStore.Append: runID and nodeID required")
	}
	if len(data) == 0 {
		return nil
	}
	prefix := []byte(runID + " " + nodeID + " | ")
	var buf bytes.Buffer
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		buf.Write(prefix)
		buf.Write(line)
		if line[len(line)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.w.Write(buf.Bytes())
	return err
}

// Read returns ErrReadUnsupported: stdout writes are not retrievable.
func (s *LogStore) Read(context.Context, string, string, storage.ReadOpts) ([]byte, error) {
	return nil, ErrReadUnsupported
}

// ReadRun returns ErrReadUnsupported for the same reason as Read.
func (s *LogStore) ReadRun(context.Context, string) ([]byte, error) {
	return nil, ErrReadUnsupported
}

// Stream returns ErrReadUnsupported. The interface allows (nil, nil)
// to signal "no streaming, poll Read instead," but on this backend
// Read also can't satisfy the caller, so the honest answer is the
// same explicit error both methods return.
func (s *LogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, ErrReadUnsupported
}

// DeleteRun is a no-op: nothing was persisted, so there is nothing to
// delete. Idempotent return matches the filesystem backend's "missing
// run is not an error" contract.
func (s *LogStore) DeleteRun(context.Context, string) error { return nil }

// CheckSpec validates that a backends.Spec for type=stdout carries no
// unexpected fields. The factory calls this so a misconfigured spec
// surfaces loudly instead of having Bucket / Path quietly ignored.
// Fields are checked in declaration order so the error message is
// deterministic.
func CheckSpec(bucket, prefix, path, url, urlSource, token string) error {
	for _, f := range []struct{ name, value string }{
		{"bucket", bucket},
		{"prefix", prefix},
		{"path", path},
		{"url", url},
		{"url_source", urlSource},
		{"token", token},
	} {
		if f.value != "" {
			return fmt.Errorf("the stdout logs backend does not accept configuration fields beyond type: (got %s=%q)", f.name, f.value)
		}
	}
	return nil
}
