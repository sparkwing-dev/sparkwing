package orchestrator

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

type heartbeatCounter struct {
	StateBackend
	nodeBeats, runBeats atomic.Int64
	err                 error
}

func (h *heartbeatCounter) TouchNodeHeartbeat(context.Context, string, string) error {
	h.nodeBeats.Add(1)
	return h.err
}

func (h *heartbeatCounter) TouchRunHeartbeat(context.Context, string) error {
	h.runBeats.Add(1)
	return h.err
}

func TestHeartbeatLoops_StopOnADeadToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &heartbeatCounter{err: &client.TokenDeadError{State: "revoked"}}
		runNodeHeartbeatLoop(t.Context(), time.Second, h, "run-1", "build", time.Hour)
		runRunHeartbeatLoop(t.Context(), time.Second, h, "run-1", time.Hour)
		if n, r := h.nodeBeats.Load(), h.runBeats.Load(); n != 2 || r != 2 {
			t.Fatalf("beat node %d and run %d times on a dead token, want 2 each (the first beat and one tick)", n, r)
		}
	})
}

func TestHeartbeatLoops_KeepBeatingThroughOtherFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &heartbeatCounter{err: errors.New("controller 500: boom")}
		ctx, cancel := context.WithTimeout(t.Context(), 5500*time.Millisecond)
		defer cancel()
		runNodeHeartbeatLoop(ctx, time.Second, h, "run-1", "build", time.Hour)
		if got := h.nodeBeats.Load(); got != 6 {
			t.Fatalf("beat %d times in 5.5s through a 500, want 6", got)
		}
	})
}

func TestFollowLogsRemote_StopsAtOnceOnADeadToken(t *testing.T) {
	const runID = "run-dead-token"
	var runReads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runs/"+runID {
			runReads.Add(1)
		}
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthenticated","token_state":"revoked","message":"token is revoked"}`)
	}))
	t.Cleanup(srv.Close)
	shortFollowTiming(t, time.Hour, time.Millisecond)

	err := followLogsRemote(context.Background(), client.NewWithToken(srv.URL, nil, ""),
		sparkwinglogs.New(srv.URL, nil, ""), runID, "", io.Discard)

	if !client.IsTokenDead(err) || !strings.Contains(err.Error(), runID) {
		t.Fatalf("follow = %v, want the dead token named with the run", err)
	}
	if got := runReads.Load(); got != 0 {
		t.Fatalf("read the run %d times after listing its nodes was refused, want 0", got)
	}
}

func TestRemoteFollowBackoff_DoublesToTheCap(t *testing.T) {
	prev := remoteFollowBackoff(0)
	for n := 1; n < 40; n++ {
		got := remoteFollowBackoff(n)
		if got > remoteFollowBackoffCap {
			t.Fatalf("failure %d waits %s, above the %s cap", n, got, remoteFollowBackoffCap)
		}
		if got != min(2*prev, remoteFollowBackoffCap) {
			t.Fatalf("failure %d waits %s after %s, want double up to the cap", n, got, prev)
		}
		prev = got
	}
	if prev != remoteFollowBackoffCap {
		t.Fatalf("backoff settled at %s, want the %s cap", prev, remoteFollowBackoffCap)
	}
}
