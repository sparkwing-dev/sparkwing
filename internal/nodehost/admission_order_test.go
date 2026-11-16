//go:build unix

package nodehost

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type capacityDispatchProbe struct {
	NativeHostStore
	blocked chan<- struct{}
}

func (s capacityDispatchProbe) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (runstate.ReconciledNode, error) {
	result, err := s.NativeHostStore.ReconcileNode(ctx, request)
	if err == nil && result.Kind == runstate.Dispatch && request.Ref.NodeID == "b" {
		select {
		case s.blocked <- struct{}{}:
		default:
		}
	}
	return result, err
}

func TestNativeOwnerAndClaimedContentionNeverHoldComputeBeforeLogicalCapacity(t *testing.T) {
	plan := capacityPlan("queue", 0, "a", "b")
	for i := range plan.Jobs {
		plan.Jobs[i].Concurrency[0].Scope = new("run")
	}
	fixture := newStoreHostFixtureForRequest(t, openStore(t), plan, runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute,
		Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
		Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:1"}},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://example.com/acme/repo.git", PipelineDir: ".sparkwing"}},
	})
	releaseRemote(t, fixture, "a")
	_, awarded := claimRemote(t, fixture, "a")
	machine := newFakeDaemon()
	claimAtCompute, allowCompute := make(chan struct{}), make(chan struct{})
	var first sync.Once
	admitter := admissionAcquireFunc(func(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
		if request.DisplayRunID == fixture.Accepted.RunID+"/a" {
			first.Do(func() { close(claimAtCompute) })
			select {
			case <-allowCompute:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
		}
		return machine.Acquire(ctx, request, queued)
	})
	config, _, _, _ := nativeHostFixture(t)
	ownerBlocked := make(chan struct{}, 1)
	config.Store, config.Owner = capacityDispatchProbe{NativeHostStore: fixture.Tenant, blocked: ownerBlocked}, ownerOperationsFixture(fixture.Host)
	config.Owner.Local = func(row runstate.NodeRow) bool { return row.Ref.NodeID == "b" }
	config.Admission = ComposeStartGate(fixture.Tenant, &LocalAdmission{Daemon: admitter}, false)
	owner, claimed, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	claimed.Acquire = func(_ context.Context, spec source.Spec, _ source.Options) (*source.Entry, error) {
		return &source.Entry{Engine: engine.Resolved{Identity: *spec.Engine}, SHA: spec.SHA, RepoKey: spec.RepoKey}, nil
	}
	execute := ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		state, err := fixture.Tenant.GetConcurrencyState(ctx, fixtureJobCapacityKey("db", "run", fixture.Accepted.RunID, ""))
		if err != nil || len(state.Holders) != 1 || state.Holders[0].NodeID != input.NodeID {
			return fmt.Errorf("execution lost logical capacity: %+v %v", state, err)
		}
		if live, _, _ := machine.counts(); live != 1 {
			return fmt.Errorf("execution lost machine admission: live=%d", live)
		}
		return fixture.report(ctx, input, runstate.NodeSuccess)
	})
	owner.Executor, claimed.Executor = execute, execute
	ctx, cancel := context.WithTimeout(t.Context(), limits.DefaultLease)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	done := make(chan error, 2)
	workers.Go(func() { done <- claimed.Execute(ctx, awarded) })
	select {
	case <-claimAtCompute:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	workers.Go(func() {
		completion, err := owner.Run(ctx, fixture.Input)
		if err == nil && completion.Outcome != "success" {
			err = fmt.Errorf("owner outcome=%s", completion.Outcome)
		}
		done <- err
	})
	select {
	case <-ownerBlocked:
	case err := <-done:
		t.Fatalf("owner did not leave the full group unclaimed: %v", err)
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	row := capacityRow(t, fixture, "b")
	if row.ClaimedBy != "" || row.ReadyAt == nil {
		t.Fatalf("owner claimed work in the full group: %+v", row)
	}
	if live, _, _ := machine.counts(); live != 0 {
		t.Errorf("owner held machine admission while waiting for logical capacity: live=%d", live)
	}
	close(allowCompute)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(context.Cause(ctx))
		}
	}
	if live, _, _ := machine.counts(); live != 0 {
		t.Fatal("machine admission leaked", live)
	}
}
