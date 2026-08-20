package orchestrator

import (
	"testing"
	"time"
)

func ExpireTestHTTPNodeLogDropCooldown(t *testing.T, nlog NodeLog) {
	t.Helper()
	log, ok := nlog.(*httpNodeLog)
	if !ok {
		t.Fatalf("node log has type %T, want *httpNodeLog", nlog)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.suppressUntil.IsZero() {
		t.Fatal("node log has no active drop cooldown")
	}
	log.suppressUntil = time.Now().Add(-time.Second)
}
