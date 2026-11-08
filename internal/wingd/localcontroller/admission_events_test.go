package localcontroller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestMachineAdmissionWaitAndGrantPersistInRunEvents(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) { binding = b; return serviceFixture(b), nil })
	seedOwner(t, binding, "run", false)
	holder := wingwire.NodeHostParticipant("run", "compile/assets")
	for _, kind := range []string{"queued", "grant"} {
		if err := c.RecordAdmissionEvent(string(holder), kind, map[string]any{"position": 2, "blocking_reason": "needs 2 cores, 1 available", "wait_ms": 1200, "blocker": "private-other-run", "lease_id": "private-lease"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.RecordAdmissionEvent(string(wingwire.NodeHostParticipant(string(wingwire.RemoteRunID("https://remote", "run")), "job")), "queued", nil); err != nil {
		t.Fatal(err)
	}
	events, err := binding.Tenant.ListEventsAfter(t.Context(), "run", 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatalf("run events: %+v %v", events, err)
	}
	for i, event := range events {
		want := []string{runstate.EventAdmissionQueued, runstate.EventAdmissionGranted}[i]
		var data struct {
			Reason string `json:"blocking_reason"`
			Wait   int    `json:"wait_ms"`
		}
		if event.Kind != want || event.NodeID != "compile/assets" || json.Unmarshal(event.Payload, &data) != nil || data.Reason != "needs 2 cores, 1 available" || data.Wait != 1200 || strings.Contains(string(event.Payload), "private") {
			t.Fatalf("admission event: %+v %s", event, event.Payload)
		}
	}
}
