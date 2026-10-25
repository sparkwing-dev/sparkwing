package controller

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func liveLine(n int) []byte { return []byte(fmt.Sprintf("line %04d\n", n)) }

func TestLiveLogs_PerNodeCapDropsTheOldestLines(t *testing.T) {
	l := newLiveLogs()
	l.perNodeBytes = 40

	for i := range 50 {
		l.appendAt("run-1", "build", "attempt", int64(i*len(liveLine(i))), liveLine(i))
	}
	chunk, ok := l.wait(t.Context(), "run-1", "build", 0)
	if !ok {
		t.Fatal("no ring for a node that wrote")
	}
	if len(chunk.Data) > l.perNodeBytes {
		t.Fatalf("ring holds %d bytes, want at most %d", len(chunk.Data), l.perNodeBytes)
	}
	if chunk.Start == 0 {
		t.Fatal("start offset stayed at 0, so nothing was evicted")
	}
	if !bytes.Contains(chunk.Data, []byte("line 0049")) {
		t.Fatalf("ring lost the newest line: %q", chunk.Data)
	}
	if bytes.Contains(chunk.Data, []byte("line 0000")) {
		t.Fatalf("ring kept the oldest line past the cap: %q", chunk.Data)
	}
	if !strings.HasSuffix(string(chunk.Data), "\n") || strings.HasPrefix(string(chunk.Data), "ine") {
		t.Fatalf("eviction cut a line in half: %q", chunk.Data)
	}
}

func TestLiveLogs_TotalCapBoundsEveryRingTogether(t *testing.T) {
	l := newLiveLogs()
	l.perNodeBytes = 1 << 20
	l.totalBytes = 2000

	for node := range 8 {
		for i := range 100 {
			l.appendAt("run-1", fmt.Sprintf("node-%d", node), "attempt", int64(i*len(liveLine(i))), liveLine(i))
		}
	}
	nodes, buffered := len(l.nodes), l.total
	if nodes != 8 {
		t.Fatalf("rings = %d, want 8", nodes)
	}
	if buffered > l.totalBytes {
		t.Fatalf("buffered %d bytes across rings, want at most %d", buffered, l.totalBytes)
	}
	chunk, ok := l.wait(t.Context(), "run-1", "node-7", 0)
	if !ok || !bytes.Contains(chunk.Data, []byte("line 0099")) {
		t.Fatalf("the newest writer lost its last line: %q", chunk.Data)
	}
}

func TestLiveLogs_IdleRingIsReleased(t *testing.T) {
	now := time.Now()
	l := newLiveLogs()
	l.idle = time.Minute
	l.now = func() time.Time { return now }

	l.appendAt("run-1", "build", "attempt", 0, liveLine(1))
	now = now.Add(2 * time.Minute)
	l.appendAt("run-1", "other", "attempt", 0, liveLine(1))
	if _, ok := l.wait(t.Context(), "run-1", "build", 0); ok {
		t.Fatal("a node that stopped writing without finishing held its ring past the idle timeout")
	}
}

func TestLiveLogs_WaitWakesOnTheNextAppend(t *testing.T) {
	l := newLiveLogs()
	l.appendAt("run-1", "build", "attempt", 0, liveLine(1))
	chunk, _ := l.wait(t.Context(), "run-1", "build", 0)

	done := make(chan liveChunk, 1)
	go func() {
		next, _ := l.wait(context.Background(), "run-1", "build", chunk.Next)
		done <- next
	}()

	l.appendAt("run-1", "build", "attempt", chunk.Next, liveLine(2))
	select {
	case got := <-done:
		if !bytes.Contains(got.Data, []byte("line 0002")) {
			t.Fatalf("waiter woke with %q, want the newly appended line", got.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never woke")
	}
}

func TestLiveLogs_NodeCapReleasesTheLeastRecentlyWrittenRing(t *testing.T) {
	now := time.Now()
	l := newLiveLogs()
	l.maxNodes = 3
	l.now = func() time.Time { return now }

	for i := range 3 {
		now = now.Add(time.Millisecond)
		l.appendAt("run-1", fmt.Sprintf("node-%d", i), "attempt", 0, liveLine(i))
	}
	now = now.Add(time.Millisecond)
	l.appendAt("run-1", "node-1", "attempt", int64(len(liveLine(1))), liveLine(99))

	now = now.Add(time.Millisecond)
	l.appendAt("run-1", "node-new", "attempt", 0, liveLine(1))

	nodes := len(l.nodes)
	if nodes != 3 {
		t.Fatalf("rings = %d, want the cap of 3", nodes)
	}
	if _, ok := l.wait(t.Context(), "run-1", "node-0", 0); ok {
		t.Error("the least recently written ring survived the cap")
	}
	for _, keep := range []string{"node-1", "node-2", "node-new"} {
		if _, ok := l.wait(t.Context(), "run-1", keep, 0); !ok {
			t.Errorf("%s lost its ring to the cap", keep)
		}
	}
}

func TestLiveLogs_WaitReturnsNotFoundOnceTheRingIsReleased(t *testing.T) {
	now := time.Now()
	l := newLiveLogs()
	l.now = func() time.Time { return now }
	l.appendAt("run-1", "build", "attempt", 0, liveLine(1))
	chunk, _ := l.wait(t.Context(), "run-1", "build", 0)

	done := make(chan bool, 1)
	go func() {
		_, ok := l.wait(context.Background(), "run-1", "build", chunk.Next)
		done <- ok
	}()

	now = now.Add(l.idle + liveLogSweepInterval + time.Second)
	l.appendAt("run-1", "other", "attempt", 0, liveLine(1))

	select {
	case ok := <-done:
		_ = ok
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never woke after its ring was released")
	}
	if _, ok := l.wait(t.Context(), "run-1", "build", 0); ok {
		t.Fatal("the released ring is still readable")
	}
}
