package wingd

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

func TestQueueBlockerSkipsUnknownResource(t *testing.T) {
	snap := admission.Snapshot{Waiters: []admission.WaiterState{{RequestID: "ahead"}, {RequestID: "waiting"}}}
	if got := queueBlocker(snap, "", "waiting"); got != "" {
		t.Fatalf("empty resource blocker = %q", got)
	}
}

func TestQueueBlockerMatchesWaiterClaims(t *testing.T) {
	snap := admission.Snapshot{Waiters: []admission.WaiterState{
		{RequestID: "cancel-others", Claims: []admission.ClaimState{{Key: "pool", Policy: admission.PolicyCancelOthers}}},
		{RequestID: "memory", MemoryBytes: 1024},
		{RequestID: "pool", Claims: []admission.ClaimState{{Key: "pool", Policy: admission.PolicyQueue}}},
		{RequestID: "cores", MilliCores: 500},
		{RequestID: "waiting"},
	}}
	for _, test := range []struct{ resource, want string }{
		{"cores", "cores"},
		{"memory", "memory"},
		{"semaphore:pool", "pool"},
		{"semaphore:missing", ""},
	} {
		if got := queueBlocker(snap, test.resource, "waiting"); got != test.want {
			t.Errorf("blocker for %q = %q, want %q", test.resource, got, test.want)
		}
	}
}
