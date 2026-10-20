package launcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLauncherPreparationTransportRequiresRetainedFence(t *testing.T) {
	fence := capability.LeaseRef{Kind: capability.UnitPlan, ID: "run", Holder: "launcher", Generation: 1}
	for _, retained := range []bool{false, true} {
		wire := launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			if len(request.Observed) != 0 || len(request.Preparing) != 1 || request.Preparing[0] != fence {
				t.Fatalf("incorrect preparation request: %+v", request)
			}
			result := controllerwire.LauncherSyncResponse{}
			if retained {
				result.Prepared = []controllerwire.LauncherPreparation{{LeaseRef: fence, ExpiresAt: time.Now().Add(time.Minute)}}
			}
			return result, nil
		}}
		control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "dev", Kinds: []capability.UnitKind{capability.UnitPlan}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = control.Prepare(t.Context(), []capability.LeaseRef{fence})
		if (err == nil) != retained {
			t.Fatalf("retained=%v error=%v", retained, err)
		}
	}
}

func TestLauncherPreparationTransportRequiresDeadline(t *testing.T) {
	fence := capability.LeaseRef{Kind: capability.UnitPlan, ID: "run", Holder: "launcher", Generation: 1}
	wire := launcherWireFake{sync: func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
		return controllerwire.LauncherSyncResponse{Prepared: []controllerwire.LauncherPreparation{{LeaseRef: fence}}}, nil
	}}
	control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "dev", Kinds: []capability.UnitKind{capability.UnitPlan}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = control.Prepare(t.Context(), []capability.LeaseRef{fence}); err == nil {
		t.Fatal("preparation accepted a fence without the store's lease deadline")
	}
}

type preparationControl struct {
	CloudControl
	prepare func(context.Context, []capability.LeaseRef) (time.Time, error)
}

func (c preparationControl) Prepare(ctx context.Context, fences []capability.LeaseRef) (time.Time, error) {
	return c.prepare(ctx, fences)
}

func TestLaunchOneKeepsLeaseDuringBoundaryPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, boundary, control := fixture()
		ready := make(chan struct{})
		var renewals atomic.Int64
		var held context.Context
		l.Control = preparationControl{CloudControl: control, prepare: func(ctx context.Context, _ []capability.LeaseRef) (time.Time, error) {
			count := renewals.Add(1)
			if count == 2 {
				held = ctx
			}
			if count == 5 {
				close(ready)
			}
			return time.Now().Add(time.Minute), nil
		}}
		l.Boundary = preparationBoundary{CloudBoundary: boundary, ready: ready}
		launched, err := l.LaunchOne(t.Context())
		if held == nil || held.Err() == nil {
			t.Fatal("preparation keeper survived publication")
		}
		if !launched || err != nil || renewals.Load() < 5 {
			t.Fatalf("blocking boundary lost preparation: launched=%v renewals=%d error=%v", launched, renewals.Load(), err)
		}
	})
}

type preparationBoundary struct {
	CloudBoundary
	ready <-chan struct{}
}

func (b preparationBoundary) Reserve(ctx context.Context, binding CloudJobBinding, name string) (CloudNodeReservation, CloudNetworkEvidence, error) {
	select {
	case <-ctx.Done():
		return CloudNodeReservation{}, CloudNetworkEvidence{}, context.Cause(ctx)
	case <-b.ready:
		return b.CloudBoundary.Reserve(ctx, binding, name)
	}
}

func TestPreparationKeeperUsesServerDeadlineAfterResponseLatency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, _, control := fixture()
		expires := time.Now().Add(30 * time.Second)
		first := true
		l.Control = preparationControl{CloudControl: control, prepare: func(context.Context, []capability.LeaseRef) (time.Time, error) {
			if first {
				first = false
				time.Sleep(20 * time.Second)
				return expires, nil
			}
			return time.Time{}, errors.New("controller unreachable")
		}}
		ctx, stop, err := l.keepPreparation(t.Context(), capability.LeaseRef{Kind: capability.UnitNode, ID: "run/job", Holder: "launcher", Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if ctx.Err() == nil {
			t.Fatal("preparation survived the store's deadline after a delayed response")
		}
	})
}

func TestPreparationKeeperRejectsAlreadyExpiredResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, _, control := fixture()
		expires := time.Now().Add(10 * time.Second)
		l.Control = preparationControl{CloudControl: control, prepare: func(context.Context, []capability.LeaseRef) (time.Time, error) {
			time.Sleep(20 * time.Second)
			return expires, nil
		}}
		_, stop, err := l.keepPreparation(t.Context(), capability.LeaseRef{Kind: capability.UnitNode, ID: "run/job", Holder: "launcher", Generation: 1})
		if stop != nil {
			stop()
		}
		if err == nil {
			t.Fatal("an expired preparation response started the boundary")
		}
	})
}
