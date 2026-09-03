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
	node    *store.Node
	pending bool
	err     error
}

func (s *stubClaimer) PrepareNodeClaim(ctx context.Context, executor client.NodeClaimExecutor) (*store.NodeSchedulingSummary, error) {
	return &store.NodeSchedulingSummary{RunID: "prepared", NodeID: "prepared", RequestedSlots: 1}, nil
}

func (s *stubClaimer) OfferNodeClaim(ctx context.Context, executor client.NodeClaimExecutor, runID, nodeID string) (client.NodeClaimOfferResult, error) {
	idx := int(s.calls.Add(1)) - 1
	if idx >= len(s.responses) {
		<-ctx.Done()
		return client.NodeClaimOfferResult{}, ctx.Err()
	}
	r := s.responses[idx]
	return client.NodeClaimOfferResult{Node: r.node, Pending: r.pending}, r.err
}

type trackingReservation struct {
	id       string
	released atomic.Int64
}

func (r *trackingReservation) ID() string             { return r.id }
func (r *trackingReservation) Release()               { r.released.Add(1) }
func (*trackingReservation) Watch(context.CancelFunc) {}

type pendingClaimer struct {
	offers []client.NodeClaimExecutor
	calls  atomic.Int64
	cancel context.CancelFunc
}

func (*pendingClaimer) PrepareNodeClaim(context.Context, client.NodeClaimExecutor) (*store.NodeSchedulingSummary, error) {
	return &store.NodeSchedulingSummary{RunID: "run", NodeID: "node", RequestedSlots: 1}, nil
}

func (c *pendingClaimer) OfferNodeClaim(_ context.Context, executor client.NodeClaimExecutor, _, _ string) (client.NodeClaimOfferResult, error) {
	c.offers = append(c.offers, executor)
	if c.calls.Add(1) == 1 {
		return client.NodeClaimOfferResult{Pending: true}, nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	return client.NodeClaimOfferResult{}, nil
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
	exec := func(ctx context.Context, n *store.Node, holderID string, reservation poolReservation) {
		executed.Add(1)
	}
	reserve := func(_ context.Context, _ store.NodeSchedulingSummary, id string) (poolReservation, bool, error) {
		return processSlotReservation{id: id}, true, nil
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

	if err := runPoolLoop(ctx, cfg, stub, exec, reserve, nil, discardLogger()); err != nil {
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
	exec := func(ctx context.Context, n *store.Node, holderID string, reservation poolReservation) {
		executed.Add(1)
	}
	reserve := func(_ context.Context, _ store.NodeSchedulingSummary, id string) (poolReservation, bool, error) {
		return processSlotReservation{id: id}, true, nil
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

	if err := runPoolLoop(ctx, cfg, stub, exec, reserve, nil, discardLogger()); err != nil {
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
	exec := func(ctx context.Context, n *store.Node, holderID string, reservation poolReservation) {
		executed.Add(1)
	}
	reserve := func(_ context.Context, _ store.NodeSchedulingSummary, id string) (poolReservation, bool, error) {
		return processSlotReservation{id: id}, true, nil
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
	if err := runPoolLoop(ctx, cfg, stub, exec, reserve, nil, discardLogger()); err != nil {
		t.Fatalf("runPoolLoop: %v", err)
	}

	if got := executed.Load(); got != 2 {
		t.Errorf("exec calls: got %d, want 2 (empty/error should not count toward MaxClaims)", got)
	}
}

func TestRunPoolSlot_DoesNotOfferWithoutAnImmediateReservation(t *testing.T) {
	stub := &stubClaimer{}
	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub", HolderPrefix: "test", MaxConcurrent: 1,
		PollInterval: time.Millisecond, SourceName: "test runner",
	})
	ctx, cancel := context.WithCancel(context.Background())
	reserve := func(_ context.Context, _ store.NodeSchedulingSummary, _ string) (poolReservation, bool, error) {
		cancel()
		return nil, false, nil
	}
	runPoolSlot(ctx, cfg, "holder", stub, nil, reserve, nil, &poolClaimBudget{}, discardLogger())
	if got := stub.calls.Load(); got != 0 {
		t.Fatalf("offer calls = %d, want 0", got)
	}
}

func TestRunPoolSlot_PinsOneReservationAcrossTheOfferRound(t *testing.T) {
	reservation := &trackingReservation{id: "local-reservation"}
	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://coordinator-a", HolderPrefix: "test", WorkerID: "worker-a",
		MaxConcurrent: 1, PollInterval: time.Millisecond, SourceName: "test runner",
	})
	ctx, cancel := context.WithCancel(context.Background())
	stub := &pendingClaimer{cancel: cancel}
	reserve := func(_ context.Context, _ store.NodeSchedulingSummary, _ string) (poolReservation, bool, error) {
		return reservation, true, nil
	}
	runPoolSlot(ctx, cfg, "coordinator-a:slot-0", stub, nil, reserve, nil, &poolClaimBudget{limit: 1}, discardLogger())
	if len(stub.offers) != 2 {
		t.Fatalf("offers = %d, want 2", len(stub.offers))
	}
	for i, offer := range stub.offers {
		if offer.HolderID != "coordinator-a:slot-0" || offer.WorkerID != "worker-a" || offer.ReservationID != reservation.id {
			t.Fatalf("offer %d = %+v", i, offer)
		}
	}
	if got := reservation.released.Load(); got != 1 {
		t.Fatalf("reservation releases = %d, want 1 after losing the offer", got)
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
