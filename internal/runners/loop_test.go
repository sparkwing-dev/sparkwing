package runners

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type claimFunc func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)

func (f claimFunc) Claim(ctx context.Context, req controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return f(ctx, req)
}

type hostFunc func(context.Context, controllerwire.ClaimedUnit) error

func (f hostFunc) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return f(ctx, unit)
}

func TestLoopUsesOneQueueForTriggerAndNode(t *testing.T) {
	want := []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}
	var requests []controllerwire.ClaimRequest
	var hosted []capability.UnitKind
	var loop *Loop
	loop = &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
		Controller: claimFunc(func(_ context.Context, req controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			requests = append(requests, req)
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: want[len(requests)-1], UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
		}),
		Host: hostFunc(func(_ context.Context, unit controllerwire.ClaimedUnit) error {
			hosted = append(hosted, unit.Unit)
			if len(hosted) == 2 {
				loop.Drain()
			}
			return nil
		}),
		CheckVersion: func(context.Context, string) error { return nil },
	}
	if err := loop.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hosted, want) {
		t.Fatalf("hosted %v", hosted)
	}
	for _, req := range requests {
		if !reflect.DeepEqual(req.Kinds, want) || req.RunnerVersion != "v1.2.3" {
			t.Fatalf("request %#v", req)
		}
	}
}

func TestLoopDrainKeepsHeldUnitsAliveAndJoins(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	joined := make(chan error, 1)
	var loop *Loop
	loop = &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
		Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
		}),
		Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
			close(started)
			<-release
			return ctx.Err()
		}),
		CheckVersion: func(context.Context, string) error { return nil },
	}
	go func() { joined <- loop.Run(t.Context()) }()
	<-started
	loop.Drain()
	select {
	case err := <-joined:
		t.Fatalf("drain did not join: %v", err)
	default:
	}
	close(release)
	if err := <-joined; err != nil {
		t.Fatalf("drain cancelled held unit: %v", err)
	}
}

func TestLoopCancellationCancelsAndJoinsHost(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	stopped := make(chan struct{})
	loop := &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
		Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
		}),
		Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}),
		CheckVersion: func(context.Context, string) error { return nil },
	}
	go func() { <-started; cancel() }()
	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("host survived loop")
	}
}

func TestLoopRefusesNewClaimsAfterVersionDenial(t *testing.T) {
	denied := errors.New("runner version revoked")
	loop := &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
		Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			t.Error("claimed under denied version")
			return controllerwire.ClaimResponse{}, nil
		}),
		Host:         hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("executed denied claim"); return nil }),
		CheckVersion: func(context.Context, string) error { return denied },
	}
	if err := loop.Run(t.Context()); !errors.Is(err, denied) {
		t.Fatalf("Run = %v", err)
	}
}

func TestLoopCannotRunTwice(t *testing.T) {
	loop := &Loop{Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1}, Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
		return controllerwire.ClaimResponse{}, nil
	}), Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { return nil }), CheckVersion: func(context.Context, string) error { return nil }}
	loop.Drain()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- loop.Run(t.Context()) })
	}
	wg.Wait()
	close(results)
	failures := 0
	for err := range results {
		if err != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("got %d failures", failures)
	}
}

func TestLoopDrainCancelsInflightClaim(t *testing.T) {
	started := make(chan struct{})
	loop := &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
		Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			close(started)
			<-ctx.Done()
			return controllerwire.ClaimResponse{}, ctx.Err()
		}),
		Host:         hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected execution"); return nil }),
		CheckVersion: func(context.Context, string) error { return nil },
	}
	go func() { <-started; loop.Drain() }()
	if err := loop.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLoopOwnsAnAwardThatRacesDrain(t *testing.T) {
	failure := errors.New("host failed before execution")
	var loop *Loop
	loop = &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
		Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			loop.Drain()
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
		}),
		Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
			if err := ctx.Err(); err != nil {
				t.Errorf("draining cancelled execution: %v", err)
			}
			return failure
		}),
		CheckVersion: func(context.Context, string) error { return nil },
	}
	if err := loop.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("lost award failure: %v", err)
	}
}

func TestLoopPreservesFractionalCapacityAndHereBinding(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	loop := &Loop{
		Config:          LoopConfig{Version: "v1.2.3", MaxConcurrent: 1, RunID: "here-run"},
		RequestCapacity: func() (float64, int64) { return .5, 100 },
		Controller: claimFunc(func(_ context.Context, req controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			if req.AvailableCores != .5 || req.AvailableMemoryBytes != 100 || req.RunID != "here-run" {
				t.Errorf("request: %#v", req)
			}
			cancel()
			return controllerwire.ClaimResponse{}, nil
		}),
		Host:         hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected host"); return nil }),
		CheckVersion: func(context.Context, string) error { return nil },
	}
	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLoopReclaimedGenerationDoesNotLoseItsAward(t *testing.T) {
	firstFinished := make(chan struct{})
	var loop *Loop
	claims := int64(0)
	var hosted atomic.Int64
	loop = &Loop{
		Config:       LoopConfig{Version: "v1.2.3", MaxConcurrent: 2},
		CheckVersion: func(context.Context, string) error { return nil },
		Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			claims++
			if claims == 2 {
				<-firstFinished
				loop.Drain()
			}
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "same-unit", RunID: "run", Capability: "secret", Binding: capability.Binding{Generation: claims}}}, nil
		}),
		Host: hostFunc(func(_ context.Context, unit controllerwire.ClaimedUnit) error {
			hosted.Add(1)
			if unit.Binding.Generation == 1 {
				close(firstFinished)
			}
			return nil
		}),
	}
	if err := loop.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hosted.Load() != 2 {
		t.Fatalf("hosted %d generations", hosted.Load())
	}
}

func TestLoopPreservesHostFailureWhenVersionPolicyAlsoFails(t *testing.T) {
	hostErr := errors.New("host cleanup failed")
	policyErr := errors.New("version revoked")
	started := make(chan struct{})
	release := make(chan struct{})
	checks := 0
	loop := &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 2},
		CheckVersion: func(context.Context, string) error {
			checks++
			if checks == 2 {
				<-started
				close(release)
				return policyErr
			}
			return nil
		},
		Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
		}),
		Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { close(started); <-release; return hostErr }),
	}
	err := loop.Run(t.Context())
	if !errors.Is(err, hostErr) || !errors.Is(err, policyErr) {
		t.Fatalf("lost drain failure: %v", err)
	}
}

func TestLoopRefreshesVersionPolicyWhileAtCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*limits.VersionIndexRefresh)
		defer cancel()
		checks, claims := 0, 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1},
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "cap"}}, nil
			}),
			Host:         hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error { <-ctx.Done(); return ctx.Err() }),
			CheckVersion: func(context.Context, string) error { checks++; return nil },
		}
		if err := loop.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run = %v", err)
		}
		if checks < 2 {
			t.Fatalf("policy refreshed %d times across two refresh intervals while saturated", checks)
		}
		if claims != 1 {
			t.Fatalf("claimed %d units with an occupied single slot", claims)
		}
	})
}
