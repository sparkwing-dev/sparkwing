package cluster

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPoolHeartbeatDefaultMatchesTheStoreCadence(t *testing.T) {
	if poolHeartbeatDefaultInterval != store.PoolHeartbeatInterval {
		t.Fatalf("pool heartbeat = %s, want the store's %s cadence",
			poolHeartbeatDefaultInterval, store.PoolHeartbeatInterval)
	}
	if poolHeartbeatDefaultInterval > store.MaxNodeHeartbeatInterval {
		t.Fatal("a pooled runner renews slower than the cadence the charge cap clears")
	}
}
