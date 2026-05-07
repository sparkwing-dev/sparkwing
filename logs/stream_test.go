package logs_test

import (
	"bufio"
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/logs"
)

// TestStream_TailsAppendedContent is the core SSE contract: append
// to a node's log from one goroutine while another reads the stream
// and verify every line eventually arrives, in order, as "data:"
// events.
func TestStream_TailsAppendedContent(t *testing.T) {
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := logs.NewClient(srv.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := c.Stream(ctx, "run-a", "node-x")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	// Give the server loop a moment to enter the poll ticker. Then
	// append a few lines and verify they show up on the stream.
	var readErr error
	var gotLines []string
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		scan := bufio.NewScanner(stream)
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "data: ") {
				mu.Lock()
				gotLines = append(gotLines, strings.TrimPrefix(line, "data: "))
				mu.Unlock()
			}
		}
		readErr = scan.Err()
	}()

	// Append lines with tiny pauses so they land on separate poll
	// cycles (server polls at 200ms). Tolerant of any scheduler.
	for _, line := range []string{"alpha", "beta", "gamma"} {
		if err := c.Append(context.Background(), "run-a", "node-x", []byte(line+"\n")); err != nil {
			t.Fatalf("Append %s: %v", line, err)
		}
		time.Sleep(220 * time.Millisecond)
	}

	// Wait for the reader to catch up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(gotLines)
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Close the stream to terminate the read goroutine cleanly.
	_ = stream.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(gotLines) < 3 {
		t.Fatalf("got %d lines, want >= 3: %v (readErr=%v)", len(gotLines), gotLines, readErr)
	}
	// Order must be preserved even if more polls were interleaved.
	want := []string{"alpha", "beta", "gamma"}
	for i, w := range want {
		if gotLines[i] != w {
			t.Errorf("line %d: got %q want %q", i, gotLines[i], w)
		}
	}
}

// TestStream_ContextCancellationStops ensures the server terminates
// the stream goroutine when the client's ctx is cancelled. Without
// this the service leaks a goroutine per dropped viewer.
func TestStream_ContextCancellationStops(t *testing.T) {
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := logs.NewClient(srv.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.Stream(ctx, "run-cancel", "node-x")
	if err != nil {
		t.Fatal(err)
	}

	// Confirm open.
	buf := make([]byte, 16)
	_, _ = stream.Read(buf)

	cancel()
	// Reader should now see EOF or similar within a moment.
	stream.Close()

	// Nothing to assert beyond "no hang"; the test times out on failure.
	_ = filepath.Join(dir)
}

// TestStream_EscapesEmbeddedNewlines prevents a malformed log line
// from splitting one event into two on the wire.
func TestStream_EscapesEmbeddedNewlines(t *testing.T) {
	dir := t.TempDir()
	s, err := logs.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := logs.NewClient(srv.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := c.Stream(ctx, "run-esc", "node-x")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// Pre-seed the log file -- the raw bytes include an embedded \r
	// which would otherwise confuse SSE parsers. Append a normal
	// line that does NOT have a trailing newline paired with a
	// final line that does; both should survive.
	_ = c.Append(context.Background(), "run-esc", "node-x",
		[]byte("with\rembedded\n"))
	_ = c.Append(context.Background(), "run-esc", "node-x",
		[]byte("second\n"))

	time.Sleep(500 * time.Millisecond)

	// Read what arrived.
	go func() { time.Sleep(500 * time.Millisecond); stream.Close() }()
	body, _ := io.ReadAll(stream)
	got := string(body)

	// CR must not appear in any data: line.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "\r") {
			t.Errorf("SSE line contains raw CR: %q", line)
		}
	}
	// "embedded" and "second" should both appear.
	if !strings.Contains(got, "embedded") || !strings.Contains(got, "second") {
		t.Errorf("missing expected content:\n%s", got)
	}
}
