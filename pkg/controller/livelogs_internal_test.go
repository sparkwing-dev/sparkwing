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
		l.Append("run-1", "build", liveLine(i))
	}
	chunk, ok := l.Read("run-1", "build", 0)
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
			l.Append("run-1", fmt.Sprintf("node-%d", node), liveLine(i))
		}
	}
	nodes, buffered := l.Stats()
	if nodes != 8 {
		t.Fatalf("rings = %d, want 8", nodes)
	}
	if buffered > l.totalBytes {
		t.Fatalf("buffered %d bytes across rings, want at most %d", buffered, l.totalBytes)
	}
	chunk, ok := l.Read("run-1", "node-7", 0)
	if !ok || !bytes.Contains(chunk.Data, []byte("line 0099")) {
		t.Fatalf("the newest writer lost its last line: %q", chunk.Data)
	}
}

func TestLiveLogs_FinishedRingIsReleasedAfterTheDrainGrace(t *testing.T) {
	now := time.Now()
	l := newLiveLogs()
	l.now = func() time.Time { return now }

	l.Append("run-1", "build", liveLine(1))
	l.Finish("run-1", "build")
	if _, ok := l.Read("run-1", "build", 0); !ok {
		t.Fatal("ring released before the drain grace, so a late reader sees nothing")
	}

	now = now.Add(liveLogDrainGrace + time.Second)
	l.Append("run-1", "other", liveLine(1))
	if _, ok := l.Read("run-1", "build", 0); ok {
		t.Fatal("finished ring survived the drain grace")
	}
	if nodes, _ := l.Stats(); nodes != 1 {
		t.Fatalf("rings = %d, want only the still-running node", nodes)
	}
}

func TestLiveLogs_IdleRingIsReleased(t *testing.T) {
	now := time.Now()
	l := newLiveLogs()
	l.idle = time.Minute
	l.now = func() time.Time { return now }

	l.Append("run-1", "build", liveLine(1))
	now = now.Add(2 * time.Minute)
	l.Append("run-1", "other", liveLine(1))
	if _, ok := l.Read("run-1", "build", 0); ok {
		t.Fatal("a node that stopped writing without finishing held its ring past the idle timeout")
	}
}

func TestLiveLogs_WaitWakesOnTheNextAppend(t *testing.T) {
	l := newLiveLogs()
	l.Append("run-1", "build", liveLine(1))
	chunk, _ := l.Read("run-1", "build", 0)

	done := make(chan liveChunk, 1)
	go func() {
		next, _ := l.Wait(context.Background(), "run-1", "build", chunk.Next)
		done <- next
	}()

	l.Append("run-1", "build", liveLine(2))
	select {
	case got := <-done:
		if !bytes.Contains(got.Data, []byte("line 0002")) {
			t.Fatalf("waiter woke with %q, want the newly appended line", got.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never woke")
	}
}

func TestLiveLogs_WaitReturnsWhenTheNodeFinishes(t *testing.T) {
	l := newLiveLogs()
	l.Append("run-1", "build", liveLine(1))
	chunk, _ := l.Read("run-1", "build", 0)

	done := make(chan liveChunk, 1)
	go func() {
		next, _ := l.Wait(context.Background(), "run-1", "build", chunk.Next)
		done <- next
	}()

	l.Finish("run-1", "build")
	select {
	case got := <-done:
		if !got.Done {
			t.Fatal("waiter woke without the done flag")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never woke on finish")
	}
}
