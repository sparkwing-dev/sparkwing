package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type stubClaimer struct {
	responses []claimResp
	calls     atomic.Int64
}

type claimResp struct {
	node *store.Node
	err  error
}

func (s *stubClaimer) ClaimNode(ctx context.Context, holderID string, labels []string, lease time.Duration, headroom *client.Headroom) (*store.Node, error) {
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

	if err := runPoolLoop(ctx, cfg, stub, exec, nil, discardLogger()); err != nil {
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

	if err := runPoolLoop(ctx, cfg, stub, exec, nil, discardLogger()); err != nil {
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
	if err := runPoolLoop(ctx, cfg, stub, exec, nil, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	if got := executed.Load(); got != 2 {
		t.Errorf("exec calls: got %d, want 2 (empty/error should not count toward MaxClaims)", got)
	}
}

func TestRunPoolLoop_RegisteredExecutorWithholdsClaimWithoutLocalCapacity(t *testing.T) {
	stub := &stubClaimer{}
	shared := make(chan struct{}, 1)
	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub", HolderPrefix: "executor:desk",
		MaxConcurrent: 1, SharedSlots: shared, PollInterval: time.Millisecond,
		LocalAdmission: true,
		ExecutorName:   "desk",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	provider := func(context.Context) capacityReport { return capacityReport{} }
	if err := runPoolLoop(ctx, cfg, stub, func(context.Context, *store.Node, string) {}, provider, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Fatalf("ClaimNode calls = %d, want 0", got)
	}
	if got := len(shared); got != 0 {
		t.Fatalf("shared slot tokens = %d after refusal, want 0", got)
	}
}

func TestRunRunnerCLI_ClaimNodesFalseRequiresTriggerLoop(t *testing.T) {
	err := runRunnerCLI([]string{
		"--controller=http://controller",
		"--metrics-addr=",
		"--claim-nodes=false",
	})
	if err == nil || !strings.Contains(err.Error(), "--claim-nodes=false requires --also-claim-triggers") {
		t.Fatalf("runRunnerCLI() error = %v, want claim-nodes/trigger-loop validation", err)
	}
}

func TestRunRunnerCLI_WarmTriggerRunnerRefusesDirectNodeClaims(t *testing.T) {
	err := runRunnerCLI([]string{
		"--controller=http://controller",
		"--metrics-addr=",
		"--also-claim-triggers",
		"--trigger-runner=warm",
	})
	if err == nil || !strings.Contains(err.Error(), "requires --claim-nodes=false") {
		t.Fatalf("runRunnerCLI() error = %v, want remote-agent race validation", err)
	}
}

func TestRunRunnerCLI_TriggerRunnerRequiresTriggerLoop(t *testing.T) {
	err := runRunnerCLI([]string{
		"--controller=http://controller",
		"--metrics-addr=",
		"--trigger-runner=warm",
		"--claim-nodes=false",
	})
	if err == nil || !strings.Contains(err.Error(), "requires --also-claim-triggers") {
		t.Fatalf("runRunnerCLI() error = %v, want trigger-loop validation", err)
	}
}

func TestRunRunnerCLI_K8sTriggerRunnerRequiresAServiceAccount(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_SA", "")
	err := runRunnerCLI([]string{
		"--controller=http://controller",
		"--metrics-addr=",
		"--also-claim-triggers",
		"--gitcache=http://cache",
		"--trigger-runner=k8s",
		"--trigger-runner-image=img",
	})
	if err == nil || !strings.Contains(err.Error(), "--trigger-runner-sa (or SPARKWING_RUNNER_SA) is required with --trigger-runner=k8s") {
		t.Fatalf("runRunnerCLI() error = %v, want the same rejection BuildK8sRunnerFactory returns", err)
	}
}

func TestRunRunnerCLI_WarmKubernetesFallbackRequiresAServiceAccount(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_SA", "")
	err := runRunnerCLI([]string{
		"--controller=http://controller",
		"--metrics-addr=",
		"--also-claim-triggers",
		"--claim-nodes=false",
		"--gitcache=http://cache",
		"--trigger-runner=warm",
		"--trigger-runner-image=img",
	})
	if err == nil || !strings.Contains(err.Error(), "--trigger-runner-sa (or SPARKWING_RUNNER_SA) is required with --trigger-runner=warm") {
		t.Fatalf("runRunnerCLI() error = %v, want warm fallback service-account validation", err)
	}
}
