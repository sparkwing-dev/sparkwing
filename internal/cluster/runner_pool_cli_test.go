package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
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
		// Exhausted responses: block until cancel so the test's bound
		// is MaxClaims, not len(responses).
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
	// 5 successive claimed results, MaxClaims=3 -> loop exits after
	// the third claim is dispatched. 4th ClaimNode should never run.
	stub := &stubClaimer{responses: []claimResp{
		{node: fakeNode("a")}, {node: fakeNode("b")}, {node: fakeNode("c")},
		{node: fakeNode("d")}, {node: fakeNode("e")},
	}}

	var executed atomic.Int64
	exec := func(ctx context.Context, n *store.Node, holderID string) {
		executed.Add(1)
	}

	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub",
		HolderPrefix:  "test",
		MaxConcurrent: 1,
		PollInterval:  time.Millisecond, // fast spin; no empty responses expected
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
	// MaxClaims=0 must not terminate the loop; it runs until ctx
	// cancels regardless of claim count. We program 50 claims and
	// bail via ctx cancel after ~100ms, expecting >20 to happen.
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
		MaxClaims:     0, // unlimited
		SourceName:    "test agent",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := runPoolLoop(ctx, cfg, stub, exec, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	// Loose bound: 100ms with microsecond-fast stubs should comfortably
	// exceed a few claims; exact count is scheduler-dependent. We just
	// want to confirm the loop did NOT stop at some implicit small N.
	if got := executed.Load(); got < 5 {
		t.Errorf("unlimited loop dispatched %d; want at least 5", got)
	}
}

func TestRunPoolLoop_EmptyPollsDoNotTickCounter(t *testing.T) {
	// The MaxClaims counter must only tick on "claimed" outcomes. A
	// transient empty poll followed by real work should still run to
	// MaxClaims successful claims, not terminate early because the
	// empty poll ate budget.
	stub := &stubClaimer{responses: []claimResp{
		{node: nil},           // empty
		{node: fakeNode("a")}, // claimed 1
		{node: nil, err: errors.New("transient")}, // error
		{node: fakeNode("b")},                     // claimed 2
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
