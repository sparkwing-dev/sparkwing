package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestConcurrencyHeartbeatInterval_FastOnlyForCancelOthers(t *testing.T) {
	cases := []struct {
		onLimit string
		want    interface{}
	}{
		{store.OnLimitCancelOthers, store.DefaultConcurrencyHeartbeatInterval},
		{store.OnLimitQueue, store.DefaultConcurrencyLease / 3},
		{store.OnLimitFail, store.DefaultConcurrencyLease / 3},
		{store.OnLimitSkip, store.DefaultConcurrencyLease / 3},
		{store.OnLimitCoalesce, store.DefaultConcurrencyLease / 3},
		{"", store.DefaultConcurrencyLease / 3},
	}
	for _, c := range cases {
		if got := store.ConcurrencyHeartbeatInterval(c.onLimit); got != c.want {
			t.Errorf("ConcurrencyHeartbeatInterval(%q) = %v, want %v", c.onLimit, got, c.want)
		}
	}
}

func TestConcurrencyHeartbeatTimeout_SlowPathOutwaitsContentionUnderInterval(t *testing.T) {
	for _, onLimit := range []string{store.OnLimitQueue, store.OnLimitFail, store.OnLimitSkip, store.OnLimitCoalesce} {
		timeout := store.ConcurrencyHeartbeatTimeout(onLimit)
		interval := store.ConcurrencyHeartbeatInterval(onLimit)
		if timeout >= interval {
			t.Errorf("%q timeout %v >= interval %v — attempts can stack", onLimit, timeout, interval)
		}
		if timeout <= store.DefaultConcurrencyHeartbeatTimeout {
			t.Errorf("%q timeout %v <= fast-path %v — too short to outwait a busy lock", onLimit, timeout, store.DefaultConcurrencyHeartbeatTimeout)
		}
	}
	if got := store.ConcurrencyHeartbeatTimeout(store.OnLimitCancelOthers); got != store.DefaultConcurrencyHeartbeatTimeout {
		t.Errorf("CancelOthers timeout = %v, want fast-path %v", got, store.DefaultConcurrencyHeartbeatTimeout)
	}
}

func TestConcurrencyHeartbeatInterval_NonCancelRefreshesWithinLease(t *testing.T) {
	for _, onLimit := range []string{store.OnLimitQueue, store.OnLimitFail, store.OnLimitSkip, store.OnLimitCoalesce} {
		interval := store.ConcurrencyHeartbeatInterval(onLimit)
		if interval >= store.DefaultConcurrencyLease {
			t.Errorf("%q interval %v >= lease %v — a live holder could lose its slot", onLimit, interval, store.DefaultConcurrencyLease)
		}
		if interval <= store.DefaultConcurrencyHeartbeatInterval {
			t.Errorf("%q interval %v <= fast cadence %v — no write reduction", onLimit, interval, store.DefaultConcurrencyHeartbeatInterval)
		}
	}
}
