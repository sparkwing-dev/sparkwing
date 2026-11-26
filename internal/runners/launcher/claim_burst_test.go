package launcher

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type queuedControl struct {
	*controlFake
	queue     []*CloudJobAward
	claimedAt []time.Time
	stopAfter int
	cancel    context.CancelFunc
}

func (q *queuedControl) Claim(context.Context, string, []string) (*CloudJobAward, time.Duration, error) {
	q.claimedAt = append(q.claimedAt, time.Now())
	if len(q.claimedAt) == q.stopAfter {
		q.cancel()
	}
	if len(q.queue) == 0 {
		return nil, 0, nil
	}
	award := q.queue[0]
	q.queue = q.queue[1:]
	return award, 0, nil
}

func fanOut(base *CloudJobAward, n int) []*CloudJobAward {
	awards := make([]*CloudJobAward, n)
	for i := range awards {
		award := *base
		award.Binding.UnitID = fmt.Sprintf("unit-%d", i)
		award.Binding.NodeID = runspec.NodeID(fmt.Sprintf("job-%d", i))
		awards[i] = &award
	}
	return awards
}

func productionBurst(ctx context.Context, t *testing.T, l *Launcher, count, stopAfter int, cancel context.CancelFunc, passes *atomic.Int32) ([]time.Time, []int32, []int32) {
	t.Helper()
	units := make([]controllerwire.ClaimedUnit, count)
	for i := range units {
		unit := wireAward(capability.UnitNode)
		unit.UnitID = fmt.Sprintf("unit-%d", i)
		unit.JobID = fmt.Sprintf("job-%d", i)
		unit.Binding.NodeID = unit.JobID
		units[i] = unit
	}
	var claimedAt []time.Time
	var claimedPasses, claimedSyncs []int32
	var syncs atomic.Int32
	control, err := NewControllerControl(launcherWireFake{
		claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			claimedAt = append(claimedAt, time.Now())
			claimedPasses = append(claimedPasses, passes.Load())
			claimedSyncs = append(claimedSyncs, syncs.Load())
			if len(claimedAt) == stopAfter {
				cancel()
			}
			if len(claimedAt) > len(units) {
				return controllerwire.ClaimResponse{}, nil
			}
			return controllerwire.ClaimResponse{Claim: &units[len(claimedAt)-1]}, nil
		},
		sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			syncs.Add(1)
			response := controllerwire.LauncherSyncResponse{}
			for _, ref := range request.Preparing {
				response.Prepared = append(response.Prepared, controllerwire.LauncherPreparation{LeaseRef: ref, ExpiresAt: time.Now().Add(time.Hour)})
			}
			for _, observation := range request.Observed {
				for _, unit := range units {
					binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
					if err != nil {
						return response, err
					}
					if binding.LeaseRef() == observation.Award {
						unit.Capability = ""
						response.Awards = append(response.Awards, unit)
						break
					}
				}
			}
			return response, nil
		},
	}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil {
		t.Fatal(err)
	}
	l.Control = control
	if err = l.Run(ctx); err != nil {
		t.Fatal(err)
	}
	return claimedAt, claimedPasses, claimedSyncs
}

func TestLauncherLaunchesWorkReadyTogetherInOneCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, _ := fixture()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var passes atomic.Int32
		kube.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			if reconcileList(action) {
				passes.Add(1)
			}
			return false, nil, nil
		})
		l.Poll = 2 * time.Second
		at, lists, syncs := productionBurst(ctx, t, l, 6, 7, cancel, &passes)
		if len(at) != 7 {
			t.Fatalf("claims=%d, want six awards and an empty response", len(at))
		}
		for i := range at {
			t.Logf("claim passes=%d syncs=%d t=%s", lists[i], syncs[i], at[i].Format("15:04:05.000"))
			if lists[i] != 1 || syncs[i] > int32(1+2*i) {
				t.Errorf("claim %d performed full reconciliation: passes=%d syncs=%d", i+1, lists[i], syncs[i])
			}
		}
		if elapsed := at[6].Sub(at[0]); elapsed != 0 {
			t.Fatalf("fan-out waited %s", elapsed)
		}
	})
}

func TestLauncherBoundsClaimsPerCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, _ := fixture()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var passes atomic.Int32
		kube.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			if reconcileList(action) {
				passes.Add(1)
			}
			return false, nil, nil
		})
		l.Poll = 2 * time.Second
		at, _, _ := productionBurst(ctx, t, l, maxClaimsPerCycle+2, maxClaimsPerCycle+1, cancel, &passes)
		if len(at) != maxClaimsPerCycle+1 {
			t.Fatalf("claims=%d", len(at))
		}
		if spread := at[maxClaimsPerCycle-1].Sub(at[0]); spread != 0 {
			t.Fatalf("first batch spread across %s", spread)
		}
		if gap := at[maxClaimsPerCycle].Sub(at[0]); gap < l.Poll {
			t.Fatalf("ninth claim bypassed bounded cycle: %s", gap)
		}
	})
}

func TestLauncherRequestsFreshCapacityInOneBoundedBurst(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "capacity requested", true: "capacity request failed"}[refused], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, boundary, _ := fixture()
				boundary.reserveErr = ErrNoDedicatedNode
				l.Poll = 5 * time.Second
				stream := watch.NewRaceFreeFake()
				kube.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) { return true, stream, nil })
				var requests []time.Time
				kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					pod := action.(ktesting.CreateAction).GetObject().(interface{ GetLabels() map[string]string })
					if pod.GetLabels()[capacityLabel] != "true" {
						return false, nil, nil
					}
					requests = append(requests, time.Now())
					if refused {
						return true, nil, fmt.Errorf("%w: capacity API unavailable", ErrNoDedicatedNode)
					}
					return false, nil, nil
				})
				ctx, cancel := context.WithCancel(t.Context())
				var passes atomic.Int32
				var claims []time.Time
				done := make(chan struct{})
				go func() {
					claims, _, _ = productionBurst(ctx, t, l, maxClaimsPerCycle+2, maxClaimsPerCycle+1, cancel, &passes)
					close(done)
				}()
				synctest.Wait()
				cancel()
				<-done
				want := maxClaimsPerCycle
				if refused {
					want = 1
				}
				if len(requests) != want || len(claims) != want {
					t.Fatalf("before the safety poll: capacity requests=%d claims=%d, want %d", len(requests), len(claims), want)
				}
				if !refused {
					if got := len(capacityPods(t, kube)); got != want {
						t.Fatalf("capacity Pods=%d, want %d", got, want)
					}
					if spread := requests[want-1].Sub(requests[0]); spread != 0 {
						t.Fatalf("capacity request burst spread across %s", spread)
					}
					t.Logf("%d capacity Pods requested at one fake-clock instant; ninth claim held for safety poll", want)
				}
			})
		})
	}
}

func TestLauncherHeldRepliesKeepSafetyReconciliationOnSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, _ := fixture()
		l.Poll = time.Second
		var passes atomic.Int32
		kube.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			if reconcileList(action) {
				passes.Add(1)
			}
			return false, nil, nil
		})
		var err error
		l.Control, err = NewControllerControl(launcherWireFake{
			claim: func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
					return controllerwire.ClaimResponse{Held: true}, nil
				case <-ctx.Done():
					return controllerwire.ClaimResponse{}, ctx.Err()
				}
			},
			sync: func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
				return controllerwire.LauncherSyncResponse{}, nil
			},
		}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		synctest.Wait()
		time.Sleep(3*time.Second + time.Nanosecond)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got := passes.Load(); got != 4 {
			t.Fatalf("frequent held replies produced %d safety reconciles, want four", got)
		}
	})
}
