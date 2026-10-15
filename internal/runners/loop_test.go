package runners

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

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
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
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
			Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
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
