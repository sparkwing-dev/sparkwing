package logs_test

import (
	"bufio"
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
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

	var readErr error
	var gotLines []string
	received := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scan := bufio.NewScanner(stream)
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "data: ") {
				gotLines = append(gotLines, strings.TrimPrefix(line, "data: "))
				if len(gotLines) >= 3 {
					select {
					case received <- struct{}{}:
					default:
					}
				}
			}
		}
		readErr = scan.Err()
	}()
	var (
		joinOnce     sync.Once
		joinErr      error
		joinReported bool
	)
	joinScanner := func() error {
		joinOnce.Do(func() {
			_ = stream.Close()
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				joinErr = errors.New("stream scanner did not stop after the response body closed")
			}
		})
		return joinErr
	}
	t.Cleanup(func() {
		if err := joinScanner(); err != nil && !joinReported {
			t.Error(err)
		}
	})

	for _, line := range []string{"alpha", "beta", "gamma"} {
		if err := c.Append(context.Background(), "run-a", "node-x", []byte(line+"\n")); err != nil {
			t.Fatalf("Append %s: %v", line, err)
		}
	}

	var waitErr error
	select {
	case <-received:
	case <-ctx.Done():
		waitErr = errors.New("stream did not deliver all appended records")
	}

	joinErr = joinScanner()
	joinReported = true
	if joinErr != nil {
		t.Fatal(joinErr)
	}
	if waitErr != nil {
		t.Fatal(waitErr)
	}

	if len(gotLines) < 3 {
		t.Fatalf("got %d lines, want >= 3: %v (readErr=%v)", len(gotLines), gotLines, readErr)
	}
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

	buf := make([]byte, 16)
	_, _ = stream.Read(buf)

	cancel()
	stream.Close()

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

	_ = c.Append(context.Background(), "run-esc", "node-x",
		[]byte("with\rembedded\n"))
	_ = c.Append(context.Background(), "run-esc", "node-x",
		[]byte("second\n"))

	var body strings.Builder
	scan := bufio.NewScanner(stream)
	for scan.Scan() {
		line := scan.Text()
		body.WriteString(line)
		body.WriteByte('\n')
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "\r") {
			t.Errorf("SSE line contains raw CR: %q", line)
		}
		got := body.String()
		if strings.Contains(got, "embedded") && strings.Contains(got, "second") {
			break
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	got := body.String()
	if !strings.Contains(got, "embedded") || !strings.Contains(got, "second") {
		t.Errorf("missing expected content:\n%s", got)
	}
}
