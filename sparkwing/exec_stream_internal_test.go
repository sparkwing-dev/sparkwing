package sparkwing

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDrainStreams_CollectsOutputBufferedBeforeAnyReaderExists(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString("buffered-line\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}

	var wg sync.WaitGroup
	var buf strings.Builder
	wg.Add(1)
	go streamChunks(withSilent(context.Background()), &wg, r, "info", nopLogger{}, &buf, nil, "test-stream")
	drainStreams(&wg, r)

	if got := buf.String(); !strings.Contains(got, "buffered-line") {
		t.Fatalf("drained output = %q, want the line buffered before the reader started", got)
	}
}

func TestDrainStreams_GivesUpWhenAWriterOutlivesTheCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	var buf strings.Builder
	wg.Add(1)
	go streamChunks(withSilent(context.Background()), &wg, r, "info", nopLogger{}, &buf, nil, "test-stream")

	returned := make(chan struct{})
	go func() {
		drainStreams(&wg, r)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(streamDrainGrace + 30*time.Second):
		t.Fatal("drainStreams never returned; a surviving writer wedges the node")
	}
}

type chunkRecorder struct{ records []LogRecord }

func (r *chunkRecorder) Log(level, message string) { r.Emit(LogRecord{Level: level, Msg: message}) }
func (r *chunkRecorder) Emit(record LogRecord)     { r.records = append(r.records, record) }

func TestStreamChunksForwardsBytesBeforeLineSplitting(t *testing.T) {
	raw := []byte{'a', 'b', '\n', 'c', 'd', 0xff, 'e'}
	logger := &chunkRecorder{}
	var captured strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	streamChunks(context.Background(), &wg, io.NopCloser(bytes.NewReader(raw)), "info", logger, &captured, nil, "one-stdout")
	if got := []byte(captured.String()); !bytes.Equal(got, raw) {
		t.Fatalf("capture = %v", got)
	}
	var forwarded []byte
	ended := 0
	for _, record := range logger.records {
		if record.Event != "exec_line" || record.streamID != "one-stdout" || record.Msg != "" {
			t.Fatalf("premature line frame: %+v", record)
		}
		forwarded = append(forwarded, record.streamData...)
		if record.streamEnd {
			ended++
		}
	}
	if !bytes.Equal(forwarded, raw) || ended != 1 {
		t.Fatalf("forwarded = %v, eof = %d", forwarded, ended)
	}
}
