package orchestrator

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSupersessionPollingCoversSlowHeartbeatPolicies(t *testing.T) {
	tests := []struct {
		policy string
		want   bool
	}{
		{policy: store.OnLimitQueue, want: true},
		{policy: store.OnLimitFail, want: true},
		{policy: store.OnLimitSkip, want: true},
		{policy: store.OnLimitCoalesce, want: true},
		{policy: store.OnLimitCancelOthers, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.policy, func(t *testing.T) {
			if got := pollsForSupersession(tt.policy); got != tt.want {
				t.Fatalf("pollsForSupersession(%q) = %v, want %v", tt.policy, got, tt.want)
			}
		})
	}
}

func TestSlotObservationDetectsOwnershipLoss(t *testing.T) {
	tests := []struct {
		name   string
		holder *store.ConcurrencyHolder
		err    error
		want   bool
	}{
		{name: "superseded", holder: &store.ConcurrencyHolder{Superseded: true}, want: true},
		{name: "active", holder: &store.ConcurrencyHolder{}, want: false},
		{name: "missing holder", want: true},
		{name: "not found", err: store.ErrNotFound, want: true},
		{name: "transient error", err: errors.New("temporary failure"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slotOwnershipLost(tt.holder, tt.err); got != tt.want {
				t.Fatalf("slotOwnershipLost(%+v, %v) = %v, want %v", tt.holder, tt.err, got, tt.want)
			}
		})
	}
}
