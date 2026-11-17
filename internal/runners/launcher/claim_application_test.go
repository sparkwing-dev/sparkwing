package launcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

type applicationControl struct {
	*controlFake
	deliveries chan *CloudJobAward
	applied    chan capability.LeaseRef
}

func (c *applicationControl) waitClaim(ctx context.Context, _ string, _ []string, _ time.Duration) claimResult {
	select {
	case award := <-c.deliveries:
		return claimResult{award: award, immediate: true}
	case <-ctx.Done():
		return claimResult{err: ctx.Err()}
	}
}

func (c *applicationControl) Prepare(ctx context.Context, refs []capability.LeaseRef) (time.Time, error) {
	c.applied <- refs[0]
	return c.controlFake.Prepare(ctx, refs)
}

func TestLauncherAppliesCompletedClaimsBeforeNextReconcile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, base := fixture()
		l.Poll = 2 * time.Second
		control := &applicationControl{controlFake: base, deliveries: make(chan *CloudJobAward), applied: make(chan capability.LeaseRef, 16)}
		l.Control = control
		var logs bytes.Buffer
		l.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var passes atomic.Int32
		reconciles := make(chan struct{}, 1)
		kube.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			if passes.Add(1) > 1 {
				select {
				case <-reconciles:
				case <-ctx.Done():
					return true, nil, ctx.Err()
				}
			}
			return false, nil, nil
		})
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		synctest.Wait()
		for i, award := range fanOut(base.award, 3) {
			<-time.NewTimer(l.Poll).C
			synctest.Wait()
			before := passes.Load()
			control.deliveries <- award
			synctest.Wait()
			<-time.NewTimer(3 * time.Second).C
			synctest.Wait()
			reconciles <- struct{}{}
			synctest.Wait()
			select {
			case ref := <-control.applied:
				if ref != award.Binding.LeaseRef() {
					t.Fatalf("application %d=%v want=%v", i, ref, award.Binding.LeaseRef())
				}
			default:
				t.Errorf("completed claim %d waited for another full reconcile (passes %d -> %d)", i, before, passes.Load())
				cancel()
				<-done
				return
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		records := startupRecords(t, &logs)
		for _, stage := range []string{"launcher_claim_received", "launcher_claim_applied"} {
			got := records[stage]
			if len(got) != 3 {
				t.Fatalf("%s records=%d want=3", stage, len(got))
			}
			for _, record := range got {
				want := float64(0)
				if stage == "launcher_claim_applied" {
					want = 3000
				}
				if record["duration_ms"] != want || record["success"] != true {
					t.Errorf("%s=%v want duration=%v success=true", stage, record, want)
				}
			}
		}
		if bytes.Contains(logs.Bytes(), base.award.Bootstrap) {
			t.Fatal("claim records contain bootstrap")
		}
	})
}

func TestLauncherRecordsClaimApplicationOnceAcrossPlacementRetries(t *testing.T) {
	l, _, boundary, _ := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	var logs bytes.Buffer
	l.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatal(err)
	}
	for range 3 {
		if err := l.placeWaiting(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	records := startupRecords(t, &logs)
	for _, stage := range []string{"launcher_claim_received", "launcher_claim_applied"} {
		if got := len(records[stage]); got != 1 {
			t.Errorf("%s records=%d want=1", stage, got)
		}
	}
	if got := len(records["launcher_preparation"]); got != 4 {
		t.Fatalf("preparation attempts=%d want=4", got)
	}
}
