package nodehost

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type burstAvailabilityWire struct {
	controllerwire.Controller
	calls *[]string
}

func (c burstAvailabilityWire) Renew(_ context.Context, _ string, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	*c.calls = append(*c.calls, "renew")
	if body.Available == nil || body.Available.Cores != 0 {
		return controllerwire.RenewalResponse{}, errors.New("zero availability lost")
	}
	return controllerwire.RenewalResponse{}, nil
}

func (c burstAvailabilityWire) ReconcileJob(context.Context, string, string, controllerwire.ReconcileRequest) (controllerwire.ReconcileResponse, error) {
	*c.calls = append(*c.calls, "reconcile")
	return controllerwire.ReconcileResponse{}, errors.New("stop")
}

type burstAvailabilityPrivate struct {
	PrivateHostState
	calls *[]string
}

func (p burstAvailabilityPrivate) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	*p.calls = append(*p.calls, "snapshot")
	return store.RunSnapshot{}, nil
}

func TestBurstAvailabilityRefreshesBeforePlacementReadsAndClaims(t *testing.T) {
	calls := []string{}
	state, err := NewRemoteHostStore(burstAvailabilityWire{calls: &calls}, burstAvailabilityPrivate{calls: &calls}, "owner", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	state.Available = func(context.Context) (*match.Resources, error) {
		calls = append(calls, "observe")
		return &match.Resources{}, nil
	}
	if _, err := state.ReadSnapshot(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}
	_, _ = state.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: store.NodeRef{RunID: "run", NodeID: "job"}})
	if !reflect.DeepEqual(calls, []string{"observe", "renew", "snapshot", "observe", "renew", "reconcile"}) {
		t.Fatalf("stale capacity used for placement: %v", calls)
	}
	calls = nil
	failure := errors.New("observation unavailable")
	state.Available = func(context.Context) (*match.Resources, error) { return nil, failure }
	if _, err := state.ReadSnapshot(t.Context(), "run"); !errors.Is(err, failure) {
		t.Fatalf("observation error lost: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("placement used failed observation: %v", calls)
	}
}
