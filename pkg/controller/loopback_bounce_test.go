package controller

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type bounceState struct {
	LoopbackState
	pending  *store.NodeBounce
	consumed string
}

func (s *bounceState) PendingNodeBounce(context.Context, string, string) (*store.NodeBounce, error) {
	return s.pending, nil
}

func (s *bounceState) ConsumeNodeBounce(_ context.Context, _, _ string, seq int64, outcome string) error {
	if s.pending == nil || s.pending.Seq != seq {
		return store.ErrNotFound
	}
	s.consumed, s.pending = outcome, nil
	return nil
}

func TestLoopbackServesBounceRequestsItsBackendHolds(t *testing.T) {
	state := &bounceState{pending: &store.NodeBounce{RunID: "run-1", NodeID: "build", Seq: 3}}
	srv := httptest.NewServer(NewLoopback(state, "run-1", "swl_bounce",
		slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer srv.Close()
	c := client.NewWithToken(srv.URL, nil, "swl_bounce")
	ctx := context.Background()
	b, err := c.PendingNodeBounce(ctx, "run-1", "build")
	if err != nil || b == nil || b.Seq != 3 {
		t.Fatalf("pending bounce = %+v, %v", b, err)
	}
	if err := c.ConsumeNodeBounce(ctx, "run-1", "build", 3, store.BounceMissed); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if state.consumed != store.BounceMissed {
		t.Fatalf("consumed outcome = %q", state.consumed)
	}
	if b, err := c.PendingNodeBounce(ctx, "run-1", "build"); err != nil || b != nil {
		t.Fatalf("after consume = %+v, %v", b, err)
	}
	if _, err := c.PendingNodeBounce(ctx, "run-2", "build"); err == nil {
		t.Fatal("read another run's bounce request")
	}
}
