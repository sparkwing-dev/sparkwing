package sparkwing

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDrainStreams_CollectsOutputBufferedBeforeAnyReaderExists writes to the
// pipe and drops the write end before any reader starts, which is the state a
// command's pipe is in once the child has echoed and exited.
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
	go streamLines(withSilent(context.Background()), &wg, r, "info", nopLogger{}, &buf)
	drainStreams(&wg, r)

	if got := buf.String(); !strings.Contains(got, "buffered-line") {
		t.Fatalf("drained output = %q, want the line buffered before the reader started", got)
	}
}

// TestDrainStreams_GivesUpWhenAWriterOutlivesTheCommand holds the write end
// open for the whole drain, standing in for a forked grandchild that still
// holds the inherited write end after its parent has been reaped.
func TestDrainStreams_GivesUpWhenAWriterOutlivesTheCommand(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	var buf strings.Builder
	wg.Add(1)
	go streamLines(withSilent(context.Background()), &wg, r, "info", nopLogger{}, &buf)

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
