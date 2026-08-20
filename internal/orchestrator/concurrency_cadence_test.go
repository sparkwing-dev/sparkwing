package orchestrator

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func SetSlotObservationIntervalForTest(t testing.TB, interval time.Duration) {
	t.Helper()
	previous := time.Duration(slotObservationIntervalNanos.Swap(int64(interval)))
	t.Cleanup(func() { slotObservationIntervalNanos.Store(int64(previous)) })
}

func TestSlotObservationIntervalsDefaultToStoreCadence(t *testing.T) {
	if got := supersessionPollInterval(); got != store.DefaultConcurrencyHeartbeatInterval {
		t.Errorf("supersession poll interval = %s, want %s", got, store.DefaultConcurrencyHeartbeatInterval)
	}
	for _, policy := range []string{"", store.OnLimitCancelOthers} {
		if got, want := slotHeartbeatInterval(policy), store.ConcurrencyHeartbeatInterval(policy); got != want {
			t.Errorf("slot heartbeat interval for %q = %s, want %s", policy, got, want)
		}
	}
}
