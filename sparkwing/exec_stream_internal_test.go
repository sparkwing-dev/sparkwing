package sparkwing

import (
	"context"
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
	go streamLines(withSilent(context.Background()), &wg, r, "info", nopLogger{}, &buf, nil)
	drainStreams(&wg, r)

	if got := buf.String(); !strings.Contains(got, "buffered-line") {
		t.Fatalf("drained output = %q, want the line buffered before the reader started", got)
	}
}

func TestDrainStreams_GivesUpWhenAWriterOutlivesTheCommand(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	var buf strings.Builder
	wg.Add(1)
	go streamLines(withSilent(context.Background()), &wg, r, "info", nopLogger{}, &buf, nil)

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
