package runners

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLoopBoundsFailuresWithoutHidingRealFailure(t *testing.T) {
	const total = 2048
	observed, claims := 0, 0
	first := errors.New("first genuine host failure")
	loop := Loop{
		Config:       LoopConfig{Version: "v1.2.3", MaxConcurrent: 1, MaxClaims: total, Kinds: []capability.UnitKind{capability.UnitTrigger}},
		CheckVersion: func(context.Context, string) error { return nil },
		Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			claims++
			return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: fmt.Sprint(claims), RunID: "run", Capability: "private"}}, nil
		}),
		Host: hostFunc(func(_ context.Context, unit controllerwire.ClaimedUnit) error {
			if unit.UnitID == "1" {
				return context.Canceled
			}
			if unit.UnitID == "2" {
				return first
			}
			return fmt.Errorf("later host failure %s", unit.UnitID)
		}),
		OnError: func(error) { observed++ },
	}
	err := loop.Run(t.Context())
	if observed != total || !errors.Is(err, first) || !errors.Is(err, context.Canceled) {
		t.Fatalf("observed=%d error=%v", observed, err)
	}
	if len(err.Error()) > 256 || !strings.Contains(err.Error(), fmt.Sprint(total-1)) {
		t.Fatalf("unbounded or incomplete shutdown summary (%d bytes): %.256s", len(err.Error()), err)
	}
}

func TestHostFailureSummaryRecognizesShutdownCause(t *testing.T) {
	cause := errors.New("runner shutdown signal")
	if !onlyCancellationCause(errors.Join(context.Canceled, cause), cause) {
		t.Fatal("shutdown cause counted as a genuine failure")
	}
	if onlyCancellationCause(errors.Join(cause, errors.New("real failure")), cause) {
		t.Fatal("genuine failure discarded")
	}
}
