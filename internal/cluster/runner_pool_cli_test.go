package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// stubClaimer implements nodeClaimer for the RunPoolLoop unit tests.
// Each call returns the next pre-programmed response so a test can
// sequence "claim / claim / empty / claim" etc. without HTTP stubs.
type stubClaimer struct {
	responses []claimResp
	calls     atomic.Int64
}

type claimResp struct {
	node *store.Node
	err  error
}

func (s *stubClaimer) ClaimNode(ctx context.Context, holderID string, labels []string, lease time.Duration) (*store.Node, error) {
	idx := int(s.calls.Add(1)) - 1
	if idx >= len(s.responses) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	r := s.responses[idx]
	return r.node, r.err
}

func fakeNode(id string) *store.Node {
	return &store.Node{RunID: "run-" + id, NodeID: id}
}

// discardLogger silences the loop's slog output during tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunPoolLoop_MaxClaimsExitsAfterN(t *testing.T) {
	stub := &stubClaimer{responses: []claimResp{
		{node: fakeNode("a")},
		{node: fakeNode("b")},
		{node: fakeNode("c")},
		{node: fakeNode("d")},
		{node: fakeNode("e")},
	}}

	var executed atomic.Int64
	exec := func(ctx context.Context, n *store.Node, holderID string) {
		executed.Add(1)
	}

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub",
		HolderPrefix:  "test",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		MaxClaims:     3,
		SourceName:    "test runner",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := runPoolLoop(ctx, cfg, stub, exec, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	if got := stub.calls.Load(); got != 3 {
		t.Errorf("ClaimNode calls: got %d, want 3", got)
	}
	if got := executed.Load(); got != 3 {
		t.Errorf("exec calls: got %d, want 3", got)
	}
}

func TestRunPoolLoop_MaxClaimsZeroIsUnlimited(t *testing.T) {
	nodes := make([]claimResp, 50)
	for i := range nodes {
		nodes[i] = claimResp{node: fakeNode("n")}
	}
	stub := &stubClaimer{responses: nodes}

	var executed atomic.Int64
	exec := func(ctx context.Context, n *store.Node, holderID string) {
		executed.Add(1)
	}

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub",
		HolderPrefix:  "test",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		MaxClaims:     0,
		SourceName:    "test agent",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := runPoolLoop(ctx, cfg, stub, exec, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	if got := executed.Load(); got < 5 {
		t.Errorf("unlimited loop dispatched %d; want at least 5", got)
	}
}

func TestRunPoolLoop_EmptyPollsDoNotTickCounter(t *testing.T) {
	stub := &stubClaimer{responses: []claimResp{
		{node: nil},
		{node: fakeNode("a")},
		{node: nil, err: errors.New("transient")},
		{node: fakeNode("b")},
	}}

	var executed atomic.Int64
	exec := func(ctx context.Context, n *store.Node, holderID string) {
		executed.Add(1)
	}

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub",
		HolderPrefix:  "test",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond,
		MaxClaims:     2,
		SourceName:    "test runner",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runPoolLoop(ctx, cfg, stub, exec, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	if got := executed.Load(); got != 2 {
		t.Errorf("exec calls: got %d, want 2 (empty/error should not count toward MaxClaims)", got)
	}
}
