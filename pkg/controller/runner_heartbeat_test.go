package controller

import (
	"fmt"
	"testing"
	"time"
)

func TestHeartbeatRegistryPrunesOldIdentitiesWhileRecording(t *testing.T) {
	r := newRunnerHeartbeatRegistry()
	started := time.Now()
	for i := range 100 {
		r.record(presenceKey{tokenPrefix: fmt.Sprintf("old-%d", i), name: "runner"}, started)
	}
	current := presenceKey{tokenPrefix: "current", name: "runner"}
	r.record(current, started.Add(2*time.Hour))
	if len(r.m) != 1 {
		t.Fatalf("retained %d heartbeat identities after two hours, want current only", len(r.m))
	}
	if seen, ok := r.lookup(current, started.Add(2*time.Hour)); !ok || !seen.Equal(started.Add(2*time.Hour)) {
		t.Fatalf("current identity = %v, %t", seen, ok)
	}
}
