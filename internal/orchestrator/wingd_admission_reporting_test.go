package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestQueueWaitHeartbeatIdentifiesInterleavedParticipants(t *testing.T) {
	var out strings.Builder
	la := &LocalAdmission{Out: &out}
	la.reportStillQueued("run-123/node-a", wingwire.Queued{Position: 30, QueueLength: 89}, time.Minute)
	la.reportStillQueued("run-123/node-b", wingwire.Queued{Position: 88, QueueLength: 89}, time.Minute)

	got := out.String()
	for _, want := range []string{
		"run-123/node-a", "position 30 of 89",
		"run-123/node-b", "position 88 of 89",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("queue output missing %q: %s", want, got)
		}
	}
}

func TestQueuePositionReportsParticipantsAhead(t *testing.T) {
	var out strings.Builder
	la := &LocalAdmission{Out: &out}
	la.reportStillQueued("run-123/node-b", wingwire.Queued{Position: 7, QueueLength: 64}, time.Minute)

	got := out.String()
	if !strings.Contains(got, "6 participants ahead") {
		t.Fatalf("queue output must identify participant positions, not claim distinct runs: %s", got)
	}
	if strings.Contains(got, "runs ahead") {
		t.Fatalf("queue output mislabels node participants as runs: %s", got)
	}
}
