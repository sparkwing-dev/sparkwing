package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDispatchedClaimHeartbeatMatchesTheStoreCadence(t *testing.T) {
	if DispatchedClaimHeartbeatInterval != store.DispatchedHeartbeatInterval {
		t.Fatalf("dispatched heartbeat = %s, want the store's %s cadence the charge cap is judged against",
			DispatchedClaimHeartbeatInterval, store.DispatchedHeartbeatInterval)
	}
	if DispatchedClaimHeartbeatInterval > store.MaxNodeHeartbeatInterval {
		t.Fatal("a dispatched node renews slower than the cadence the charge cap clears")
	}
}
