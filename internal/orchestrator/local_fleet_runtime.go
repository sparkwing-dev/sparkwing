package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var errFleetLocalStoreRequired = errors.New("foreground fleet requires the local SQLite state backend")

type localFleetRuntime struct {
	config fleet.Config
	store  *store.Store
}

func prepareLocalFleetRuntime(backends Backends, opts Options) (*localFleetRuntime, error) {
	if opts.FleetConfigPath == "" {
		return nil, errors.New("fleet coordinator config path is required")
	}
	cfg, err := fleet.Load(opts.FleetConfigPath, fleet.LocalTailscaleIPs)
	if err != nil {
		return nil, err
	}
	if !backends.LocalCoordination || backends.APISocket != "" {
		return nil, errFleetLocalStoreRequired
	}
	var st *store.Store
	switch state := canonicalState(backends.State).(type) {
	case localState:
		st = state.st
	case *localState:
		st = state.st
	}
	if st == nil {
		return nil, errFleetLocalStoreRequired
	}
	return &localFleetRuntime{config: cfg, store: st}, nil
}

func (f *localFleetRuntime) start(runID string, opts *Options, fallback runner.Runner, logger *slog.Logger) (*localFleetAuthority, runner.Runner, error) {
	authority, err := startLocalFleetAuthority(f.store, runID, f.config, opts, logger)
	if err != nil {
		return nil, nil, err
	}
	labels := fleetCoordinatorLabels(f.config.Local.Capabilities)
	local := &limitedFleetRunner{runner: fallback, slots: make(chan struct{}, f.config.Local.MaxConcurrent), labels: labels}
	coordinator := localStoreFleetCoordinator{store: f.store}
	return authority, warmpool.New(coordinator, local, warmpool.Config{FallbackLabels: labels}, logger), nil
}

func fleetCoordinatorLabels(capabilities []string) []string {
	labels := make([]string, 0, len(capabilities)+2)
	seen := make(map[string]struct{}, len(capabilities)+2)
	for _, label := range append(append([]string(nil), capabilities...), "local", "location=coordinator") {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	return labels
}

type limitedFleetRunner struct {
	runner runner.Runner
	slots  chan struct{}
	labels []string
}

func (r *limitedFleetRunner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	slot := fleetRunnerSlot{slots: r.slots}
	if !slot.acquire(ctx) {
		return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
	}
	defer slot.release()
	releaseWorker := req.ReleaseWorkerSlot
	reacquireWorker := req.ReacquireWorkerSlot
	req.ReleaseWorkerSlot = func() {
		slot.release()
		if releaseWorker != nil {
			releaseWorker()
		}
	}
	req.ReacquireWorkerSlot = func() bool {
		if reacquireWorker != nil && !reacquireWorker() {
			return false
		}
		if slot.acquire(ctx) {
			return true
		}
		if releaseWorker != nil {
			releaseWorker()
		}
		return false
	}
	return r.runner.RunNode(ctx, req)
}

func (r *limitedFleetRunner) AdvertisedLabels() []string {
	return append([]string(nil), r.labels...)
}

type fleetRunnerSlot struct {
	mu    sync.Mutex
	slots chan struct{}
	held  bool
}

func (s *fleetRunnerSlot) acquire(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held {
		return true
	}
	select {
	case s.slots <- struct{}{}:
		s.held = true
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *fleetRunnerSlot) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held {
		<-s.slots
		s.held = false
	}
}

type localStoreFleetCoordinator struct {
	store *store.Store
}

func (c localStoreFleetCoordinator) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	return c.store.MarkNodeReady(ctx, runID, nodeID)
}

func (c localStoreFleetCoordinator) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return c.store.UpdateNodeActivity(ctx, runID, nodeID, detail)
}

func (c localStoreFleetCoordinator) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return c.store.TouchNodeHeartbeat(ctx, runID, nodeID)
}

func (c localStoreFleetCoordinator) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	return c.store.GetNode(ctx, runID, nodeID)
}

func (c localStoreFleetCoordinator) RevokeNodeReady(ctx context.Context, runID, nodeID string) (bool, error) {
	return c.store.RevokeNodeReady(ctx, runID, nodeID)
}

func (c localStoreFleetCoordinator) FinalizeNodeReady(ctx context.Context, runID, nodeID string) (store.ExecutorClaimRoundResult, error) {
	return c.store.FinalizeExecutorClaimRound(ctx, runID, nodeID)
}

var (
	_ runner.Runner          = (*limitedFleetRunner)(nil)
	_ runner.LabelAdvertiser = (*limitedFleetRunner)(nil)
)
