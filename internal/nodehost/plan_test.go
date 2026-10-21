package nodehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestSDKPlanningUnknownPipelineNeverPlansOrCaches(t *testing.T) {
	planner, input, fixture, processes := sdkPlanningFixture(t)
	input.Run.Pipeline = "ghost"
	_, err := planner.Prepare(t.Context(), input, fixture.input)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.UnknownPipeline {
		t.Fatalf("error=%v", err)
	}
	if len(*processes) != 1 || len(fixture.cache.puts) != 0 {
		t.Fatal("unknown pipeline started planning or populated the cache")
	}
	process := (*processes)[0]
	if process.closed != 1 || process.cleaned != 1 {
		t.Fatal("unknown pipeline leaked its describe process")
	}
	for _, request := range process.requests {
		if request.Op == "plan" {
			t.Fatal("unknown pipeline reached planning")
		}
	}
}

type leaseAtNativeFinish struct {
	*ownerFixture
	daemon  *fakeDaemon
	testing *testing.T
}

func (s leaseAtNativeFinish) FinishRunAndUnit(ctx context.Context, request store.FinishRunRequest) (store.Applied, error) {
	if live, _, _ := s.daemon.counts(); live != 1 {
		s.testing.Errorf("live admission leases at finish=%d, want 1", live)
	}
	return s.ownerFixture.FinishRunAndUnit(ctx, request)
}

func TestOwnerRunAdmissionOutlivesRunFinish(t *testing.T) {
	fixture, host, input := newOwnerFixture(t, true)
	daemon := newFakeDaemon()
	host.Store = leaseAtNativeFinish{ownerFixture: fixture, daemon: daemon, testing: t}
	configureNativeAdmission(&host, &LocalAdmission{Daemon: daemon}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := host.Run(ctx, input); err != nil {
		t.Fatal(err)
	}
	if fixture.finished.Transition.RunID != runspec.RunID(input.RunID) {
		t.Fatal("run did not finish")
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 1 || released != 1 {
		t.Fatalf("admission leases live/granted/released=%d/%d/%d", live, granted, released)
	}
}
