package controller_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newLiveLogFixture(t *testing.T) ownershipFixture {
	t.Helper()
	return newOwnershipFixtureWithScopes(t, []string{
		controller.ScopeNodesClaim,
		controller.ScopeRunsState,
		controller.ScopeRunsRead,
	})
}

func newLiveLogFixtureWithLimits(t *testing.T, perNode int, total int64, maxNodes int, idle time.Duration) ownershipFixture {
	t.Helper()
	return newOwnershipFixtureWith(t, []string{
		controller.ScopeNodesClaim,
		controller.ScopeRunsState,
		controller.ScopeRunsRead,
	}, func(s *controller.Server) *controller.Server {
		return s.WithLiveLogLimits(perNode, total, maxNodes, idle)
	})
}

func TestLiveLog_AppendNeedsTheNodesOwnClaim(t *testing.T) {
	f := newLiveLogFixture(t)
	const path = "/api/v1/runs/run-1/nodes/only/logs"

	if got := f.post(t, f.stranger, path, `{"msg":"from nowhere"}`); got != http.StatusForbidden {
		t.Errorf("stranger POST %s = %d, want 403", path, got)
	}
	if got := f.post(t, f.owner, path, `{"msg":"first"}`); got != http.StatusNoContent {
		t.Errorf("claiming runner POST %s = %d, want 204", path, got)
	}

	chunk, err := client.NewWithToken(f.url, nil, f.owner).
		ReadNodeLiveLog(context.Background(), "run-1", "only", 0)
	if err != nil {
		t.Fatalf("ReadNodeLiveLog: %v", err)
	}
	if !strings.Contains(chunk.Data, `"first"`) {
		t.Fatalf("live read returned %q, want the posted line", chunk.Data)
	}
	if chunk.Done {
		t.Error("live read reports the node done while it is still running")
	}
	if chunk.Next <= 0 {
		t.Errorf("next offset = %d, want the end of the posted line", chunk.Next)
	}
}

func TestLiveLog_ReadIsNotFoundForANodeThatNeverWrote(t *testing.T) {
	f := newLiveLogFixture(t)
	_, err := client.NewWithToken(f.url, nil, f.owner).
		ReadNodeLiveLog(context.Background(), "run-1", "only", 0)
	if err == nil {
		t.Fatal("live read of a silent node succeeded, want a not-found so the caller uses the durable copy")
	}
}

func TestLiveLog_ReaderSeesLinesAppendedAfterItConnected(t *testing.T) {
	f := newLiveLogFixture(t)
	const path = "/api/v1/runs/run-1/nodes/only/logs"
	if got := f.post(t, f.owner, path, `{"msg":"before"}`); got != http.StatusNoContent {
		t.Fatalf("seed POST = %d, want 204", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, err := client.NewWithToken(f.url, nil, f.owner).
		StreamNodeLiveLog(ctx, "run-1", "only", 0)
	if err != nil {
		t.Fatalf("StreamNodeLiveLog: %v", err)
	}
	defer func() { _ = body.Close() }()

	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	if got := waitForSSEData(t, lines, "before"); !got {
		t.Fatal("stream never delivered the line written before the reader connected")
	}
	if got := f.post(t, f.owner, path, `{"msg":"after"}`); got != http.StatusNoContent {
		t.Fatalf("second POST = %d, want 204", got)
	}
	if got := waitForSSEData(t, lines, "after"); !got {
		t.Fatal("stream never delivered the line written after the reader connected")
	}
}

func TestLiveLog_FinishingTheNodeEndsTheStream(t *testing.T) {
	f := newLiveLogFixture(t)
	if got := f.post(t, f.owner, "/api/v1/runs/run-1/nodes/only/logs", `{"msg":"only line"}`); got != http.StatusNoContent {
		t.Fatalf("seed POST = %d, want 204", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := client.NewWithToken(f.url, nil, f.owner)
	body, err := c.StreamNodeLiveLog(ctx, "run-1", "only", 0)
	if err != nil {
		t.Fatalf("StreamNodeLiveLog: %v", err)
	}
	defer func() { _ = body.Close() }()

	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	if !waitForSSEData(t, lines, "only line") {
		t.Fatal("stream never delivered the buffered line")
	}

	claimCtx := store.WithNodeClaimFence(context.Background(), f.fence)
	if err := c.FinishNode(claimCtx, "run-1", "only", "success", "", nil); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	if !waitForSSELine(t, lines, "event: stream_end") {
		t.Fatal("finishing the node did not end the stream")
	}
}

func waitForSSEData(t *testing.T, lines <-chan string, want string) bool {
	t.Helper()
	return waitForSSELine(t, lines, "data: ", want)
}

func waitForSSELine(t *testing.T, lines <-chan string, want ...string) bool {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false
			}
			matched := true
			for _, w := range want {
				if !strings.Contains(line, w) {
					matched = false
					break
				}
			}
			if matched {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestLiveLog_AppendRefusesANodeTheRunDoesNotHave(t *testing.T) {
	f := newLiveLogFixture(t)
	for _, path := range []string{
		"/api/v1/runs/run-1/nodes/invented/logs",
		"/api/v1/runs/run-2/nodes/only/logs",
	} {
		got := f.post(t, f.owner, path, `{"msg":"nowhere"}`)
		if got == http.StatusNoContent {
			t.Errorf("POST %s = 204; a node the run does not have must not mint a buffer", path)
		}
	}
	if _, err := client.NewWithToken(f.url, nil, f.owner).
		ReadNodeLiveLog(context.Background(), "run-1", "invented", 0); !errors.Is(err, client.ErrNoLiveLog) {
		t.Fatalf("live read of the invented node = %v, want ErrNoLiveLog", err)
	}
}

func TestLiveLog_ReaderIsToldWhenTheBufferDroppedBytesItHadNotRead(t *testing.T) {
	f := newLiveLogFixtureWithLimits(t, 256, 1<<20, 16, time.Minute)
	const path = "/api/v1/runs/run-1/nodes/only/logs"
	if got := f.post(t, f.owner, path, `{"msg":"oldest"}`); got != http.StatusNoContent {
		t.Fatalf("seed POST = %d, want 204", got)
	}
	for i := range 40 {
		if got := f.post(t, f.owner, path, fmt.Sprintf(`{"msg":"filler %02d"}`, i)); got != http.StatusNoContent {
			t.Fatalf("filler POST = %d, want 204", got)
		}
	}

	chunk, err := client.NewWithToken(f.url, nil, f.owner).
		ReadNodeLiveLog(context.Background(), "run-1", "only", 0)
	if err != nil {
		t.Fatalf("ReadNodeLiveLog: %v", err)
	}
	if chunk.Start == 0 {
		t.Fatal("the buffer never evicted, so the gap cannot be reported")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, err := client.NewWithToken(f.url, nil, f.owner).
		StreamNodeLiveLog(ctx, "run-1", "only", 0)
	if err != nil {
		t.Fatalf("StreamNodeLiveLog: %v", err)
	}
	defer func() { _ = body.Close() }()

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	if !waitForSSEData(t, lines, "dropped") {
		t.Fatal("the stream never told the reader bytes were dropped before its first line")
	}
}
