package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	errLocalLogWrite = errors.New("no space left on device")
	errLocalLogClose = errors.New("close: input/output error")
)

type stubLogFile struct {
	buf      bytes.Buffer
	writeErr error
	closeErr error
	closes   int
}

func (f *stubLogFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.buf.Write(p)
}

func (f *stubLogFile) Close() error {
	f.closes++
	return f.closeErr
}

func newStubNodeLogger(nodeID string, f *stubLogFile) *nodeLogger {
	return &nodeLogger{file: f, enc: json.NewEncoder(f), nodeID: nodeID}
}

func TestNodeLoggerReportsLocalWriteFailuresAsDrops(t *testing.T) {
	nl := newStubNodeLogger("only", &stubLogFile{writeErr: errLocalLogWrite})
	for i := 0; i < 3; i++ {
		nl.Log("info", "a line the disk refused")
	}

	count, reason := nodeLogDrops(nl)
	if count != 3 {
		t.Errorf("nodeLogDrops count = %d, want 3 (one per line the file rejected)", count)
	}
	if !strings.Contains(reason, errLocalLogWrite.Error()) {
		t.Errorf("nodeLogDrops reason = %q, want the write error", reason)
	}
}

func TestNodeLoggerCloseFailureIsADrop(t *testing.T) {
	f := &stubLogFile{closeErr: errLocalLogClose}
	nl := newStubNodeLogger("only", f)
	nl.Log("info", "a line the file took")

	if err := nl.Close(); !errors.Is(err, errLocalLogClose) {
		t.Errorf("Close error = %v, want the close failure", err)
	}
	count, reason := nodeLogDrops(nl)
	if count != 1 || !strings.Contains(reason, errLocalLogClose.Error()) {
		t.Errorf("nodeLogDrops = (%d, %q), want the close failure counted once", count, reason)
	}

	if err := nl.Close(); err != nil {
		t.Errorf("second Close error = %v, want nil: the executor's deferred close must be a no-op", err)
	}
	if f.closes != 1 {
		t.Errorf("file closed %d times, want 1", f.closes)
	}
	if count, _ := nodeLogDrops(nl); count != 1 {
		t.Errorf("nodeLogDrops count after the second close = %d, want 1", count)
	}
}

func TestNodeLoggerMarshalFailureDropsOneLine(t *testing.T) {
	f := &stubLogFile{}
	nl := newStubNodeLogger("only", f)

	nl.Emit(sparkwing.LogRecord{Level: "info", Msg: "unencodable", Attrs: map[string]any{"n": math.NaN()}})
	count, reason := nodeLogDrops(nl)
	if count != 1 {
		t.Errorf("nodeLogDrops count = %d, want 1 for the record that would not encode", count)
	}
	if !strings.Contains(reason, "NaN") {
		t.Errorf("nodeLogDrops reason = %q, want the encoding failure", reason)
	}

	nl.Log("info", "the next line still lands")
	if !strings.Contains(f.buf.String(), "the next line still lands") {
		t.Errorf("a record that would not encode must not stop the writer, got: %q", f.buf.String())
	}
	if count, _ := nodeLogDrops(nl); count != 1 {
		t.Errorf("nodeLogDrops count = %d after a good line, want 1", count)
	}
}
