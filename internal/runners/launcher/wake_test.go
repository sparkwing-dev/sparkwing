package launcher

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestCapacityWakePublishesBeforePollAndPollRecoversMissedWake(t *testing.T) {
	for _, mode := range []string{"watch", "missed watch", "closed watch", "unavailable watch", "cancelled award"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, boundary, control := fixture()
				l.Poll = 5 * time.Second
				boundary.reserveErr = ErrNoDedicatedNode
				award := control.award
				control.award = nil
				l.Control = wakeAllocationControl{CloudControl: control, resources: &match.Resources{Cores: award.CPUCores, MemoryBytes: award.Memory}}
				if _, err := l.launch(t.Context(), award); !errors.Is(err, ErrNoDedicatedNode) {
					t.Fatal(err)
				}
				events := watch.NewRaceFreeFake()
				kube.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) {
					if mode == "unavailable watch" {
						return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("watch unavailable"))
					}
					return true, events, nil
				})
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- l.Run(ctx) }()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				synctest.Wait()
				time.Sleep(time.Second)
				boundary.reserveErr = nil
				if mode == "closed watch" {
					events.Stop()
				}
				if mode == "cancelled award" {
					control.remove = true
				}
				if mode == "watch" || mode == "cancelled award" {
					events.Modify(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: capacityName(award.Binding), Namespace: "cloud", Labels: map[string]string{capacityLabel: "true"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
					synctest.Wait()
				} else {
					time.Sleep(4 * time.Second)
					synctest.Wait()
				}
				_, err := kube.BatchV1().Jobs("cloud").Get(ctx, jobName(award.Binding), metav1.GetOptions{})
				if mode == "cancelled award" {
					if !apierrors.IsNotFound(err) || len(l.waitingAwards()) != 0 {
						t.Fatalf("wake executed a cancelled award: %v", err)
					}
				} else if err != nil {
					t.Fatalf("capacity available but Job not published: %v", err)
				}
				if (mode == "watch" || mode == "cancelled award") && control.claims != 1 {
					t.Fatalf("watch bypassed claim cooldown: %d claims", control.claims)
				}
			})
		})
	}
}

func TestRunHoldsClaimWithoutBlockingReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, _ := fixture()
		l.Poll = 5 * time.Second
		unit := wireAward(capability.UnitNode)
		ready := make(chan struct{})
		calls := 0
		var wait time.Duration
		wire := launcherWireFake{
			claim: func(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				wait = request.Wait
				if calls == 1 {
					select {
					case <-ready:
						return controllerwire.ClaimResponse{Claim: &unit}, nil
					case <-ctx.Done():
						return controllerwire.ClaimResponse{}, ctx.Err()
					}
				}
				<-ctx.Done()
				return controllerwire.ClaimResponse{}, ctx.Err()
			},
			sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
				response := controllerwire.LauncherSyncResponse{}
				for _, ref := range request.Preparing {
					response.Prepared = append(response.Prepared, controllerwire.LauncherPreparation{LeaseRef: ref, ExpiresAt: unit.LeaseExpiresAt})
				}
				for range request.Observed {
					u := unit
					u.Capability = ""
					response.Awards = append(response.Awards, u)
				}
				return response, nil
			},
		}
		var err error
		l.Control, err = NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		passes := 0
		kube.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { passes++; return false, nil, nil })
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if wait != l.Poll {
			t.Errorf("claim wait=%s, want safety poll %s", wait, l.Poll)
		}
		time.Sleep(l.Poll)
		synctest.Wait()
		if passes < 2 {
			t.Error("held claim blocked reconciliation")
		}
		time.Sleep(time.Millisecond)
		start := time.Now()
		close(ready)
		synctest.Wait()
		binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kube.BatchV1().Jobs("cloud").Get(ctx, jobName(binding), metav1.GetOptions{}); err != nil {
			t.Fatalf("ready signal did not publish Job: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("ready signal waited %s", elapsed)
		}
	})
}

type wakeAllocationControl struct {
	CloudControl
	resources *match.Resources
}

func (c wakeAllocationControl) Sync(ctx context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	states, err := c.CloudControl.Sync(ctx, observations)
	for i := range states {
		states[i].Resources = c.resources
	}
	return states, err
}
