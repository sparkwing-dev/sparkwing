package logs_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func TestStream_TailsAppendedContent(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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

func TestStream_EscapesEmbeddedNewlines(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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

func streamEvents(t *testing.T, srvURL, runID, nodeID string) (<-chan string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	body, err := logs.NewClient(srvURL, nil).Stream(ctx, runID, nodeID)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	events := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		scan := bufio.NewScanner(body)
		scan.Buffer(nil, 1<<20)
		for scan.Scan() {
			if line, ok := strings.CutPrefix(scan.Text(), "data: "); ok {
				select {
				case events <- line:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = body.Close()
		<-done
	})
	return events, cancel
}

func nextEvent(t *testing.T, events <-chan string) string {
	t.Helper()
	line, ok := <-events
	if !ok {
		t.Fatal("stream ended early")
	}
	return line
}

func writeRunFile(t *testing.T, root, rel, data string) {
	t.Helper()
	path := filepath.Join(root, "runs", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func newStreamServer(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	s, err := logs.New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return root, srv.URL
}

func TestStreamSendsHistoryAcrossReadChunks(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	root, url := newStreamServer(t)
	var history strings.Builder
	var want []string
	for i := range 40 {
		line := fmt.Sprintf("%d:%s", i, strings.Repeat("x", i*997))
		want = append(want, line)
		history.WriteString(line + "\n")
	}
	want = append(want, "", "carriage")
	history.WriteString("\ncarri\rage\r\nunterminated")
	writeRunFile(t, root, "run/node.log", history.String())
	events, _ := streamEvents(t, url, "run", "node")
	for i, w := range want {
		if got := nextEvent(t, events); got != w {
			t.Fatalf("event %d = %.40q (len %d), want %.40q (len %d)", i, got, len(got), w, len(w))
		}
	}
	writeRunFile(t, root, "run/node.log", " tail\n")
	if got := nextEvent(t, events); got != "unterminated tail" {
		t.Fatalf("completed partial = %q", got)
	}
}

func TestStreamKeepsPartialLinesPerAttributedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	root, url := newStreamServer(t)
	writeRunFile(t, root, "run/node.log", "first\nlegacy-")
	writeRunFile(t, root, "run/.attempts/node.log/a1.log", "attempt-")
	events, _ := streamEvents(t, url, "run", "node")
	if got := nextEvent(t, events); got != "first" {
		t.Fatalf("history = %q", got)
	}
	writeRunFile(t, root, "run/node.log", "done\n")
	writeRunFile(t, root, "run/.attempts/node.log/a1.log", "done\n")
	got := []string{nextEvent(t, events), nextEvent(t, events)}
	slices.Sort(got)
	if got[0] != "attempt-done" || got[1] != "legacy-done" {
		t.Fatalf("completed partials = %q", got)
	}
}

func TestStreamReadsOnlyAppendedBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1s of real work; the fast class runs under -short")
	}
	const size = 2 << 20
	root, url := newStreamServer(t)
	line := strings.Repeat("x", 63) + "\n"
	writeRunFile(t, root, "run/node.log", strings.Repeat(line, size/len(line)-1)+"last\n")
	events, _ := streamEvents(t, url, "run", "node")
	for nextEvent(t, events) != "last" {
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	// safety: each round completes a poll on which the large legacy file is unchanged.
	const rounds = 5
	for i := range rounds {
		tick := fmt.Sprintf("tick-%d", i)
		writeRunFile(t, root, "run/.attempts/node.log/a1.log", tick+"\n")
		if got := nextEvent(t, events); got != tick {
			t.Fatalf("event = %q, want %q", got, tick)
		}
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d polls over an unchanged %d byte file allocated %d bytes", rounds, size, allocated)
	if allocated > size {
		t.Fatalf("%d polls over an unchanged %d byte file allocated %d bytes", rounds, size, allocated)
	}
}

func TestStreamHandlerReturnsWhenClientCancels(t *testing.T) {
	root := t.TempDir()
	s, err := logs.New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	body, err := logs.NewClient(srv.URL, nil).Stream(ctx, "run", "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := body.Read(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = body.Close()
	// safety: Close waits for every active handler, so a stream that ignores
	// cancellation holds this test until the package timeout.
	srv.Close()
}
