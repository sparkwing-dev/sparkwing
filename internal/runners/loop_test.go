package runners

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type claimFunc func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)

func (f claimFunc) Claim(ctx context.Context, req controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return f(ctx, req)
}

type hostFunc func(context.Context, controllerwire.ClaimedUnit) error

func (f hostFunc) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return f(ctx, unit)
}

func TestLoopUsesOneQueueForEveryUnitKind(t *testing.T) {
	want := []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode}
	var requests []controllerwire.ClaimRequest
	var hosted []capability.UnitKind
	hostedUnit := make(chan struct{}, 1)
	var loop *Loop
	loop = &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
		Controller: claimFunc(func(_ context.Context, req controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			if len(requests) > 0 {
				<-hostedUnit
			}
			requests = append(requests, req)
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: want[len(requests)-1], UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
		}),
		Host: hostFunc(func(_ context.Context, unit controllerwire.ClaimedUnit) error {
			hosted = append(hosted, unit.Unit)
			if len(hosted) == len(want) {
				loop.Drain()
			}
			hostedUnit <- struct{}{}
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
	for i, req := range requests {
		if !slices.Contains(req.Kinds, want[i]) || req.RunnerVersion != "v1.2.3" {
			t.Fatalf("request %#v", req)
		}
	}
}

func TestLoopOwnerDoesNotOccupyItsJobsOnlySlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		jobFinished := make(chan struct{})
		claims := 0
		var ownerFinished, jobRan bool
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 2},
			CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				unit := capability.UnitTrigger
				if claims == 2 {
					unit = capability.UnitNode
					if !reflect.DeepEqual(request.Kinds, []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}) {
						t.Errorf("owner occupied its capacity but claim kinds were %v", request.Kinds)
					}
				}
				if request.FreeSlots == nil || *request.FreeSlots != 1 {
					t.Errorf("claim %d free slots = %v", claims, request.FreeSlots)
				}
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: unit, UnitID: string(unit), RunID: "run", JobID: "greet", Capability: "private"}}, nil
			}),
			Host: hostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
				if unit.Unit == capability.UnitTrigger {
					select {
					case <-jobFinished:
						ownerFinished = true
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				jobRan = true
				close(jobFinished)
				return nil
			}),
		}
		if err := loop.Run(ctx); err != nil || !jobRan || !ownerFinished {
			t.Fatalf("single-slot run stalled: claims=%d greet ran=%v owner finished=%v error=%v", claims, jobRan, ownerFinished, err)
		}
	})
}

func TestLoopAwaitingJobDoesNotOccupyItsChildsOnlySlot(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				childFinished := make(chan struct{})
				claims := 0
				var parentFinished, childRan bool
				loop := &Loop{
					Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 2, Kinds: []capability.UnitKind{kind}},
					CheckVersion: func(context.Context, string) error { return nil },
					Controller: claimFunc(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						claims++
						if request.FreeSlots == nil || *request.FreeSlots != 1 {
							t.Errorf("claim %d free slots = %v", claims, request.FreeSlots)
						}
						run := "parent"
						if claims == 2 {
							run = "child"
						}
						return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: kind, UnitID: run, RunID: run, JobID: "job", Capability: "private"}}, nil
					}),
					Host: hostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
						if unit.RunID == "child" {
							childRan = true
							close(childFinished)
							return nil
						}
						resume := nodehost.AwaitSlot(ctx)()
						defer resume(ctx)
						select {
						case <-childFinished:
							parentFinished = true
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}),
				}
				if err := loop.Run(ctx); err != nil || !childRan || !parentFinished {
					t.Fatalf("single-slot RunAndAwait stalled: claims=%d child ran=%v parent finished=%v error=%v", claims, childRan, parentFinished, err)
				}
			})
		})
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

func TestLoopOwnsAwardReturnedWhileClaimDeadlineCancelsTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hosted := 0
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, ClaimUntil: time.Now().Add(time.Minute)},
			CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				<-ctx.Done()
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "committed", RunID: "run", Capability: "private"}}, nil
			}),
			Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				hosted++
				return ctx.Err()
			}),
		}
		if err := loop.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		if hosted != 1 {
			t.Fatalf("hosted %d awards, want the committed award", hosted)
		}
	})
}

func TestLoopPreservesHereBinding(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	loop := &Loop{
		Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1, RunID: "here-run"},
		Controller: claimFunc(func(_ context.Context, req controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			if req.RunID != "here-run" {
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

func FuzzLoopHereSelection(f *testing.F) {
	f.Add("run", false)
	f.Add("run/other", true)
	f.Add("run:\x00\u2603", false)
	f.Fuzz(func(t *testing.T, suffix string, foreign bool) {
		run := "here:" + suffix
		claims, hosted := 0, 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.2.3", RunID: run, MaxConcurrent: 1, MaxClaims: 1},
			Controller: claimFunc(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				if request.RunID != run || request.JobID != "" || !reflect.DeepEqual(request.Kinds, []capability.UnitKind{capability.UnitTrigger}) {
					t.Error("request escaped named trigger selection")
					return controllerwire.ClaimResponse{}, errors.New("request escaped named trigger selection")
				}
				awarded := run
				if foreign {
					awarded += "\x00"
				}
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "unit", RunID: awarded, Capability: "private"}}, nil
			}),
			Host: hostFunc(func(_ context.Context, unit controllerwire.ClaimedUnit) error {
				hosted++
				if unit.RunID != run || foreign {
					return errors.New("foreign award reached host")
				}
				return nil
			}),
			CheckVersion: func(context.Context, string) error { return nil },
		}
		err := loop.Run(t.Context())
		wantHosted := 1
		if foreign {
			wantHosted = 0
		}
		if (err == nil) == foreign || claims != 1 || hosted != wantHosted {
			t.Fatalf("foreign=%v error=%v claims=%d hosted=%d", foreign, err, claims, hosted)
		}
	})
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
		OnError: func(err error) {
			if !errors.Is(err, hostErr) {
				t.Errorf("reported host error = %v", err)
			}
		},
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

func TestLoopClaimBudgetDrainsWithoutAnotherRequest(t *testing.T) {
	claims, hosted := 0, 0
	loop := &Loop{
		Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 2, Kinds: []capability.UnitKind{capability.UnitNode}},
		CheckVersion: func(context.Context, string) error { return nil },
		Controller: claimFunc(func(_ context.Context, req controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			claims++
			if claims > 2 {
				return controllerwire.ClaimResponse{}, errors.New("claim budget ignored")
			}
			if !reflect.DeepEqual(req.Kinds, []capability.UnitKind{capability.UnitNode}) {
				t.Fatalf("claim kinds %v", req.Kinds)
			}
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
		}),
		Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { hosted++; return nil }),
	}
	if err := loop.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if claims != 2 || hosted != 2 {
		t.Fatalf("claims=%d hosted=%d", claims, hosted)
	}
}

func TestLoopHonorsConfiguredPollInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		start := time.Now()
		calls := 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: 17 * time.Second}, CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				if request.MaxConcurrent != 1 {
					t.Errorf("configured concurrency = %d, want 1", request.MaxConcurrent)
				}
				calls++
				if calls == 2 {
					if time.Since(start) != 17*time.Second {
						t.Errorf("poll after %v", time.Since(start))
					}
					cancel()
				}
				return controllerwire.ClaimResponse{}, nil
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
		}
		if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestLoopWaitsOutAControllerRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		start := time.Now()
		calls := 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Second}, CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				if calls == 1 {
					return controllerwire.ClaimResponse{}, &apierr.Error{Code: hosterr.RateLimited, Message: "poll later", Details: map[string]any{"retry_after_ms": float64(5000)}}
				}
				if time.Since(start) != 5*time.Second {
					t.Errorf("repolled after %v, want the controller's 5s", time.Since(start))
				}
				cancel()
				return controllerwire.ClaimResponse{}, nil
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
		}
		if err := loop.Run(ctx); !errors.Is(err, context.Canceled) || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestLoopPollsThroughControllerRestarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls, hosted := 0, 0
		start := time.Now()
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 1, PollInterval: time.Second},
			CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				switch calls {
				case 1, 2, 3, 5, 6, 7:
					return controllerwire.ClaimResponse{}, fmt.Errorf("controller socket: %w", &net.OpError{Op: "dial", Net: "unix", Err: os.ErrNotExist})
				case 4:
					return controllerwire.ClaimResponse{}, nil
				default:
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
				}
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { hosted++; return nil }),
		}
		if err := loop.Run(t.Context()); err != nil || calls != 8 || hosted != 1 || (time.Since(start) < 7700*time.Millisecond || time.Since(start) > 8400*time.Millisecond) {
			t.Fatalf("claims = %d, hosted = %d, elapsed = %s, error = %v", calls, hosted, time.Since(start), err)
		}
	})
}

func TestLoopBoundsControllerTransportRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		start := time.Now()
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Second},
			CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				return controllerwire.ClaimResponse{}, io.ErrUnexpectedEOF
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
		}
		if err := loop.Run(t.Context()); !errors.Is(err, io.ErrUnexpectedEOF) || (calls < 100 || calls > 205) || time.Since(start) != 10*time.Minute {
			t.Fatalf("claims = %d, elapsed = %s, error = %v", calls, time.Since(start), err)
		}
	})
}

func TestLoopControllerRestartKeepsHeldHostAliveThroughDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, recovered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		joined := make(chan error, 1)
		calls := 0
		start := time.Now()
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, PollInterval: time.Second},
			CheckVersion: func(context.Context, string) error { return nil },
			Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				close(started)
				<-release
				return ctx.Err()
			}),
		}
		loop.Controller = claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			calls++
			if calls == 1 {
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
			}
			<-started
			if calls < 5 {
				return controllerwire.ClaimResponse{}, &net.OpError{Op: "dial", Net: "unix", Err: os.ErrNotExist}
			}
			loop.Drain()
			close(recovered)
			return controllerwire.ClaimResponse{}, nil
		})
		go func() { joined <- loop.Run(t.Context()) }()
		<-recovered
		synctest.Wait()
		select {
		case err := <-joined:
			t.Fatalf("drain returned before its held host: %v", err)
		default:
		}
		close(release)
		if err := <-joined; err != nil || calls != 5 || (time.Since(start) < 3350*time.Millisecond || time.Since(start) > 3700*time.Millisecond) {
			t.Fatalf("claims = %d, elapsed = %s, error = %v", calls, time.Since(start), err)
		}
	})
}

func TestLoopTransportRecoveryKeepsRefusalsFatal(t *testing.T) {
	for _, code := range []apierr.Code{hosterr.Unauthenticated, hosterr.Forbidden, apierr.ProtocolError} {
		t.Run(string(code), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				failure := &apierr.Error{Code: code, Message: "refused"}
				loop := &Loop{
					Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Second},
					CheckVersion: func(context.Context, string) error { return nil },
					Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						calls++
						if calls == 1 {
							return controllerwire.ClaimResponse{}, io.EOF
						}
						return controllerwire.ClaimResponse{}, failure
					}),
					Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
				}
				if err := loop.Run(t.Context()); !errors.Is(err, failure) || calls != 2 {
					t.Fatalf("claims = %d, error = %v", calls, err)
				}
			})
		})
	}
}

func TestLoopHostCompletionKeepsServerBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var refusedAt time.Time
		calls := 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, PollInterval: time.Second}, CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				switch calls {
				case 1:
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
				case 2:
					refusedAt = time.Now()
					return controllerwire.ClaimResponse{}, &apierr.Error{Code: hosterr.RateLimited, Message: "poll later", Details: map[string]any{"retry_after_ms": float64(60000)}}
				}
				if waited := time.Since(refusedAt); waited < time.Minute {
					t.Errorf("claimed %v after a 60s refusal", waited)
				}
				cancel()
				return controllerwire.ClaimResponse{}, nil
			}),
			Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
				}
				return nil
			}),
		}
		if err := loop.Run(ctx); !errors.Is(err, context.Canceled) || calls != 3 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestLoopRefusesAwardOutsideRequestedSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config LoopConfig
		award  controllerwire.ClaimedUnit
	}{
		{"here", LoopConfig{RunID: "here"}, controllerwire.ClaimedUnit{Unit: capability.UnitNode, RunID: "another"}},
		{"kind", LoopConfig{Kinds: []capability.UnitKind{capability.UnitNode}}, controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, RunID: "run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.config.Version, tc.config.MaxConcurrent = "v1.0.0", 1
			tc.award.UnitID, tc.award.Capability = "unit", "private"
			loop := &Loop{
				Config: tc.config, CheckVersion: func(context.Context, string) error { return nil },
				Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					return controllerwire.ClaimResponse{Claim: &tc.award}, nil
				}),
				Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error {
					t.Error("executed outside requested selection")
					return nil
				}),
			}
			if err := loop.Run(t.Context()); err == nil {
				t.Fatal("accepted mismatched award")
			}
		})
	}
}

func TestLoopLongPollDoesNotDelayLifecycleOrPolicy(t *testing.T) {
	t.Run("poll_interval", func(t *testing.T) { testLoopLifecycleDeadline(t, false) })
	t.Run("blocked_transport", func(t *testing.T) { testLoopLifecycleDeadline(t, true) })
}

func TestLoopPendingClaimSurvivesPolicyRefresh(t *testing.T) {
	for _, here := range []bool{false, true} {
		for _, awarded := range []bool{false, true} {
			testPendingClaimRefresh(t, "run", here, 2, awarded)
		}
	}
}

func TestLoopPendingAwardPreservesPolicyDenial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		denied := errors.New("runner version revoked")
		checks, hosted := 0, 0
		loop := Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 1},
			CheckVersion: func(context.Context, string) error {
				checks++
				if checks > 1 {
					return denied
				}
				return nil
			},
			Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				<-ctx.Done()
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "committed", RunID: "run", Capability: "private"}}, nil
			}),
			Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				hosted++
				return ctx.Err()
			}),
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*limits.VersionIndexRefresh)
		defer cancel()
		err := loop.Run(ctx)
		if hosted != 1 || !errors.Is(err, denied) {
			t.Fatalf("hosted=%d error=%v, want committed award drained and policy denial returned", hosted, err)
		}
	})
}

func TestLoopPendingClaimStopOutcomes(t *testing.T) {
	for reason, name := range []string{"policy", "cancellation", "idle", "claim_deadline", "version", "drain"} {
		t.Run(name, func(t *testing.T) {
			for order := range uint8(3) {
				for _, awarded := range []bool{false, true} {
					testPendingClaimStop(t, "run", uint8(reason), order, awarded, true, false)
				}
			}
		})
	}
}

func FuzzLoopPendingClaimStopOutcomes(f *testing.F) {
	for reason := range uint8(6) {
		f.Add("run", reason, reason%3, true, false, false)
	}
	f.Fuzz(func(t *testing.T, run string, reason, order uint8, awarded, hostFails, cancelJoining bool) {
		testPendingClaimStop(t, "run:"+run, reason%6, order%3, awarded, hostFails, cancelJoining)
	})
}

func testPendingClaimStop(t *testing.T, run string, reason, order uint8, awarded, hostFails, cancelJoining bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		started, cancelled := make(chan struct{}), make(chan struct{})
		deliver, returned := make(chan struct{}), make(chan struct{})
		hostStarted, hostFinish := make(chan struct{}), make(chan struct{})
		var deliverOnce sync.Once
		release := func() { deliverOnce.Do(func() { close(deliver) }) }
		denied, cancelledErr, hostErr := errors.New("policy denied"), errors.New("caller cancelled"), errors.New("host failed")
		if reason == 4 {
			denied = errRunnerUpdate
		}
		checks, claims, hosted := 0, 0, 0
		loop := Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: int(order % 2), RunID: run},
			CheckVersion: func(context.Context, string) error {
				checks++
				if (reason == 0 || reason == 4) && checks > 1 {
					if order < 2 {
						release()
						if order == 0 {
							<-returned
						}
					}
					return denied
				}
				return nil
			},
			Controller: claimFunc(func(claimCtx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				if claims != 1 {
					return controllerwire.ClaimResponse{}, errors.New("claimed after stop")
				}
				defer close(returned)
				go func() { <-claimCtx.Done(); close(cancelled) }()
				close(started)
				<-deliver
				if !awarded {
					return controllerwire.ClaimResponse{}, claimCtx.Err()
				}
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "unit", RunID: run, Capability: "private"}}, nil
			}),
			Host: hostFunc(func(hostCtx context.Context, unit controllerwire.ClaimedUnit) error {
				hosted++
				if unit.RunID != run || (reason != 1 && hostCtx.Err() != nil) {
					t.Error("award identity or held context changed")
				}
				close(hostStarted)
				<-hostFinish
				if hostFails {
					return hostErr
				}
				return nil
			}),
		}
		if reason == 2 {
			loop.Config.IdleExit = time.Minute
		}
		if reason == 3 {
			loop.Config.ClaimUntil = time.Now().Add(time.Minute)
		}
		finished := make(chan error, 1)
		go func() { finished <- loop.Run(ctx) }()
		<-started
		if reason == 1 || reason == 5 {
			if order == 0 {
				release()
			}
			if reason == 1 {
				cancel(cancelledErr)
			} else {
				loop.Drain()
			}
			if order == 1 {
				release()
			}
		}
		<-cancelled
		release()
		if awarded {
			<-hostStarted
			synctest.Wait()
			select {
			case err := <-finished:
				t.Fatalf("returned before host joined: %v", err)
			default:
			}
			if cancelJoining {
				cancel(cancelledErr)
			}
			close(hostFinish)
		}
		err := <-finished
		wantDenied := reason == 0 || reason == 4
		wantCancelled := reason == 1 || (awarded && cancelJoining)
		wantHostErr := awarded && hostFails
		wantHosted := 0
		if awarded {
			wantHosted = 1
		}
		if claims != 1 || hosted != wantHosted || errors.Is(err, denied) != wantDenied || errors.Is(err, cancelledErr) != wantCancelled || errors.Is(err, hostErr) != wantHostErr || (err == nil) != (!wantDenied && !wantCancelled && !wantHostErr) {
			t.Fatalf("reason=%d order=%d awarded=%t claims=%d hosted=%d error=%v", reason, order, awarded, claims, hosted, err)
		}
	})
}

func FuzzLoopPendingClaimNoReplay(f *testing.F) {
	f.Add("run", true, uint8(2), false)
	f.Add("run/other:\x00", false, uint8(3), true)
	f.Fuzz(func(t *testing.T, run string, here bool, refreshes uint8, awarded bool) {
		testPendingClaimRefresh(t, "run:"+run, here, int(refreshes%4)+1, awarded)
	})
}

func testPendingClaimRefresh(t *testing.T, run string, here bool, refreshes int, awarded bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*limits.VersionIndexRefresh)
		defer cancel()
		respond := make(chan struct{})
		lost := errors.New("committed claim response lost")
		claims, checks, hosted := 0, 0, 0
		loop := Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 1},
			CheckVersion: func(context.Context, string) error {
				checks++
				if checks == refreshes+1 {
					close(respond)
				}
				return nil
			},
			Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				if claims != 1 {
					return controllerwire.ClaimResponse{}, errors.New("uncertain claim replayed")
				}
				select {
				case <-respond:
					if awarded {
						return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "unit", RunID: run, Capability: "private"}}, nil
					}
					return controllerwire.ClaimResponse{}, lost
				case <-ctx.Done():
					return controllerwire.ClaimResponse{}, ctx.Err()
				}
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { hosted++; return nil }),
		}
		if here {
			loop.Config.RunID = run
		}
		err := loop.Run(ctx)
		wantHosted := 0
		if awarded {
			wantHosted = 1
		}
		if (awarded && err != nil) || (!awarded && !errors.Is(err, lost)) || claims != 1 || hosted != wantHosted || checks != refreshes+1 {
			t.Fatalf("here=%v awarded=%v error=%v claims=%d hosted=%d checks=%d", here, awarded, err, claims, hosted, checks)
		}
	})
}

func testLoopLifecycleDeadline(t *testing.T, blocked bool) {
	for _, name := range []string{"idle", "claim_deadline", "policy"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				cfg := LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: 24 * time.Hour}
				want := time.Minute
				switch name {
				case "idle":
					cfg.IdleExit = want
				case "claim_deadline":
					cfg.ClaimUntil = start.Add(want)
				case "policy":
					want = limits.VersionIndexRefresh
				}
				policyErr := errors.New("version revoked")
				checks, claims := 0, 0
				loop := &Loop{
					Config: cfg, CheckVersion: func(context.Context, string) error {
						checks++
						if name == "policy" && checks > 1 {
							return policyErr
						}
						return nil
					},
					Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						claims++
						if blocked {
							<-ctx.Done()
							return controllerwire.ClaimResponse{}, ctx.Err()
						}
						return controllerwire.ClaimResponse{}, nil
					}),
					Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*want)
				defer cancel()
				err := loop.Run(ctx)
				if name == "policy" {
					if !errors.Is(err, policyErr) {
						t.Fatalf("policy error=%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if elapsed := time.Since(start); elapsed != want {
					t.Errorf("stopped after %s, want %s", elapsed, want)
				}
				if claims != 1 {
					t.Errorf("claims=%d", claims)
				}
			})
		})
	}
}

func TestLoopObservesHostCompletionDuringBlockedClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), 2*limits.VersionIndexRefresh)
		defer cancel()
		pending, finishHost := make(chan struct{}), make(chan struct{})
		claims, joined := 0, false
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, IdleExit: time.Minute},
			CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				if request.FreeSlots == nil || *request.FreeSlots != 3-claims {
					t.Errorf("poll %d free slots: %v", claims, request.FreeSlots)
				}
				if claims == 1 {
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
				}
				close(pending)
				<-ctx.Done()
				joined = true
				return controllerwire.ClaimResponse{}, ctx.Err()
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { <-finishHost; return nil }),
		}
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()
		<-pending
		synctest.Wait()
		close(finishHost)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != time.Minute || !joined || claims != 2 {
			t.Fatalf("idle exit=%s, transport joined=%t, claims=%d", elapsed, joined, claims)
		}
	})
}

func TestLoopRefreshDuringBlockedClaimDrainsHeldHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*limits.VersionIndexRefresh)
		defer cancel()
		policyErr := errors.New("runner version revoked")
		finishHost := make(chan struct{})
		checks, claims := 0, 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2},
			CheckVersion: func(context.Context, string) error {
				checks++
				if checks == 3 {
					close(finishHost)
					return policyErr
				}
				return nil
			},
			Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				if claims == 1 {
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
				}
				<-ctx.Done()
				return controllerwire.ClaimResponse{}, ctx.Err()
			}),
			Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				select {
				case <-finishHost:
				case <-ctx.Done():
				}
				if ctx.Err() != nil {
					t.Error("policy refresh cancelled held work")
				}
				return ctx.Err()
			}),
		}
		if err := loop.Run(ctx); !errors.Is(err, policyErr) {
			t.Fatalf("policy failure lost: %v", err)
		}
		if claims != 2 {
			t.Fatalf("claims=%d, want one award and one cancelled request", claims)
		}
	})
}

func TestLoopDrainDuringPendingClaimPolicyRefresh(t *testing.T) {
	for mode, name := range []string{"cancelled", "wrapped", "joined_cancellation", "denied", "joined_denial", "wrapped_joined_denial"} {
		t.Run(name, func(t *testing.T) {
			for _, awarded := range []bool{false, true} {
				synctest.Test(t, func(t *testing.T) {
					refreshing := make(chan struct{})
					denied := errors.New("runner version refused")
					checks, hosted := 0, 0
					loop := Loop{
						Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1},
						CheckVersion: func(ctx context.Context, _ string) error {
							checks++
							if checks == 1 {
								return nil
							}
							close(refreshing)
							<-ctx.Done()
							switch mode {
							case 0:
								return ctx.Err()
							case 1:
								return fmt.Errorf("policy refresh: %w", ctx.Err())
							case 2:
								return errors.Join(ctx.Err(), fmt.Errorf("policy refresh: %w", ctx.Err()))
							case 3:
								return denied
							case 4:
								return errors.Join(ctx.Err(), denied)
							default:
								return fmt.Errorf("policy refresh: %w", errors.Join(ctx.Err(), denied))
							}
						},
						Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
							<-ctx.Done()
							if awarded {
								return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
							}
							return controllerwire.ClaimResponse{}, ctx.Err()
						}),
						Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
							hosted++
							return ctx.Err()
						}),
					}
					finished := make(chan error, 1)
					go func() { finished <- loop.Run(t.Context()) }()
					<-refreshing
					loop.Drain()
					err := <-finished
					wantHosted := 0
					if awarded {
						wantHosted = 1
					}
					if hosted != wantHosted || (mode < 3 && err != nil) || (mode >= 3 && !errors.Is(err, denied)) {
						t.Fatalf("awarded=%t hosted=%d policy error=%v", awarded, hosted, err)
					}
				})
			}
		})
	}
}

type pendingPolicyCause []string

func (c pendingPolicyCause) Error() string { return c[0] }

func TestLoopPendingPolicyAcceptsArbitraryCancellationCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		refreshing := make(chan struct{})
		checks := 0
		loop := Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1},
			CheckVersion: func(ctx context.Context, _ string) error {
				checks++
				if checks == 1 {
					return nil
				}
				close(refreshing)
				<-ctx.Done()
				return context.Cause(ctx)
			},
			Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				<-ctx.Done()
				return controllerwire.ClaimResponse{}, ctx.Err()
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { return nil }),
		}
		finished := make(chan error, 1)
		go func() {
			defer func() {
				if v := recover(); v != nil {
					finished <- fmt.Errorf("Run panicked: %v", v)
				}
			}()
			finished <- loop.Run(ctx)
		}()
		<-refreshing
		cancel(pendingPolicyCause{"operator shutdown"})
		var cause pendingPolicyCause
		if err := <-finished; !errors.As(err, &cause) || cause[0] != "operator shutdown" {
			t.Fatalf("Run = %v, want operator shutdown cause", err)
		}
	})
}

func TestLoopDrainedCompletionRepollsBeforeIdleExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finishHost := make(chan struct{})
		policyBlocked, releasePolicy := make(chan struct{}), make(chan struct{})
		claims, checks := 0, 0
		loop := &Loop{
			Config: LoopConfig{
				Version: "v1.0.0", MaxConcurrent: 2, MaxClaims: 2,
				PollInterval: time.Hour, IdleExit: time.Minute,
			},
			CheckVersion: func(context.Context, string) error {
				checks++
				if checks == 3 {
					close(policyBlocked)
					<-releasePolicy
				}
				return nil
			},
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				if claims == 2 {
					return controllerwire.ClaimResponse{}, nil
				}
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{
					Unit: capability.UnitNode, UnitID: fmt.Sprint(claims), RunID: "run", Capability: "private",
				}}, nil
			}),
			Host: hostFunc(func(_ context.Context, unit controllerwire.ClaimedUnit) error {
				if unit.UnitID == "1" {
					<-finishHost
				}
				return nil
			}),
		}
		done := make(chan error, 1)
		go func() { done <- loop.Run(t.Context()) }()
		<-policyBlocked
		close(finishHost)
		synctest.Wait()
		start := time.Now()
		close(releasePolicy)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if claims != 3 || !time.Now().Equal(start) {
			t.Fatalf("completion left capacity idle: claims=%d elapsed=%v", claims, time.Since(start))
		}
	})
}

func TestRunnerReportsAKubernetesPodToTheController(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	if !InKubernetesPod() {
		t.Fatal("the kubelet's service host did not mark a pod")
	}
	for _, pod := range []bool{false, true} {
		if got := (&Loop{Config: LoopConfig{Kubernetes: pod}}).request().Kubernetes; got != pod {
			t.Fatalf("claim request kubernetes = %v, want %v", got, pod)
		}
	}
}

func TestLoopReportsStoppedClaimsBeforeHeldHostJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		calls := 0
		loop := &Loop{
			Config:          LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, PollInterval: time.Second},
			CheckVersion:    func(context.Context, string) error { return nil },
			OnClaimsStopped: func() { close(stopped) },
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				if calls == 1 {
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "secret"}}, nil
				}
				return controllerwire.ClaimResponse{}, io.ErrUnexpectedEOF
			}),
			Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				close(started)
				<-release
				if ctx.Err() != nil {
					t.Errorf("claim transport exit cancelled held work: %v", ctx.Err())
				}
				return nil
			}),
		}
		joined := make(chan error, 1)
		go func() { joined <- loop.Run(t.Context()) }()
		<-started
		<-stopped
		select {
		case err := <-joined:
			t.Fatalf("returned before held host: %v", err)
		default:
		}
		close(release)
		if err := <-joined; !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("claim transport outcome: %v", err)
		}
	})
}

func TestLoopAwaitResumeWaitsForRunnerSlot(t *testing.T) {
	for _, scenario := range []string{"working", "cancelled", "claim-pending"} {
		cancelled := scenario == "cancelled"
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				finishChild, releaseOther := make(chan struct{}), make(chan struct{})
				resumed := make(chan struct{})
				returnClaim, otherStarted := make(chan struct{}), make(chan struct{})
				claims := 0
				loop := &Loop{
					Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, MaxClaims: 2, Kinds: []capability.UnitKind{capability.UnitNode}},
					CheckVersion: func(context.Context, string) error { return nil },
					Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						claims++
						if claims == 2 && scenario == "claim-pending" {
							close(finishChild)
							<-returnClaim
						}
						id := fmt.Sprint(claims)
						return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: id, RunID: id, Capability: "private"}}, nil
					}),
					Host: hostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
						if unit.RunID == "2" {
							if scenario != "claim-pending" {
								close(finishChild)
							}
							close(otherStarted)
							<-releaseOther
							return nil
						}
						resume := nodehost.AwaitSlot(ctx)()
						<-finishChild
						resume(ctx)
						close(resumed)
						return ctx.Err()
					}),
				}
				done := make(chan error, 1)
				go func() { done <- loop.Run(ctx) }()
				<-finishChild
				synctest.Wait()
				select {
				case <-resumed:
					t.Error("parent resumed while another job held the only slot")
				default:
				}
				if scenario == "claim-pending" {
					close(returnClaim)
					<-otherStarted
					synctest.Wait()
					select {
					case <-resumed:
						t.Error("pending award lost its reserved slot to a resume")
					default:
					}
				}
				if cancelled {
					cancel()
					synctest.Wait()
					select {
					case <-resumed:
					default:
						t.Error("cancelled resume retained its slot wait")
					}
				}
				close(releaseOther)
				if err := <-done; (err != nil) != cancelled {
					t.Fatal(err)
				}
				select {
				case <-resumed:
				default:
					t.Fatal("parent failed to resume after capacity returned")
				}
			})
		})
	}
}

func TestLoopBoundsQueuedFanoutAndKeepsChildPlanningLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const capacity, fanout = 2, 20
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		claimed := 0
		planned := false
		childAwarded := false
		var suspended, returned atomic.Int64
		maximum := 0
		group := make(chan struct{}, 1)
		group <- struct{}{}
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: capacity, MaxClaims: fanout + 1, Kinds: []capability.UnitKind{capability.UnitNode, capability.UnitPlan}},
			CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				kind, id := capability.UnitPlan, "awaited-child"
				if slices.Contains(request.Kinds, capability.UnitNode) && claimed < fanout {
					claimed++
					kind, id = capability.UnitNode, fmt.Sprintf("queued-%d", claimed)
				} else if childAwarded {
					return controllerwire.ClaimResponse{}, nil
				} else {
					childAwarded = true
				}
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: kind, UnitID: id, RunID: id, JobID: "capacity-one-group", Capability: "private"}}, nil
			}),
			Host: hostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
				if unit.Unit == capability.UnitPlan {
					planned = true
					maximum = int(suspended.Load())
					cancel()
					return nil
				}
				resume, stop := nodehost.AdmissionWait(ctx)
				if resume == nil {
					returned.Add(1)
					return nil
				}
				suspended.Add(1)
				defer func() { suspended.Add(-1); stop() }()
				select {
				case group <- struct{}{}:
					resume(ctx)
					<-group
				case <-ctx.Done():
				}
				return nil
			}),
		}
		if err := loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if maximum > capacity || returned.Load() == 0 || !planned {
			t.Fatalf("admission races escaped the waiter bound: maximum=%d cap=%d returned=%d child planned=%t", maximum, capacity, returned.Load(), planned)
		}
	})
}

func TestLoopQueuedCapStillRunsAnAwaitedChildJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		var planned atomic.Bool
		var childRan bool
		queued := 0
		planAwarded, childAwarded := false, false
		loop := &Loop{
			Config:       LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, Kinds: []capability.UnitKind{capability.UnitNode, capability.UnitPlan}},
			CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				kind, id := capability.UnitPlan, "child-plan"
				if planned.Load() {
					if childAwarded || !slices.Contains(request.Kinds, capability.UnitNode) {
						return controllerwire.ClaimResponse{}, nil
					}
					childAwarded = true
					kind, id = capability.UnitNode, "child-stamp"
				} else if slices.Contains(request.Kinds, capability.UnitNode) && queued < 2 {
					queued++
					kind, id = capability.UnitNode, fmt.Sprintf("queued-%d", queued)
				} else if planAwarded {
					return controllerwire.ClaimResponse{}, nil
				} else {
					planAwarded = true
				}
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: kind, UnitID: id, RunID: id, JobID: "job", Capability: "private"}}, nil
			}),
			Host: hostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
				switch unit.UnitID {
				case "child-plan":
					planned.Store(true)
					return nil
				case "child-stamp":
					childRan = true
					cancel()
					return nil
				default:
					_, stop := nodehost.AdmissionWait(ctx)
					defer stop()
					<-ctx.Done()
					return nil
				}
			}),
		}
		if err := loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if !planned.Load() || !childRan {
			t.Fatalf("admission cap starved awaited child execution: planned=%t child ran=%t queued=%d", planned.Load(), childRan, queued)
		}
	})
}
