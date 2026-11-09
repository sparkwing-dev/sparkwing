package opsview

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestAdmissionWaitLine(t *testing.T) {
	clear := int64(100000)
	state := wingwire.QueueState{
		Holders: []wingwire.Holder{
			{Pipeline: "pre-push", Resources: wingwire.HostResources{Cores: 5.2}},
			{Pipeline: "docs-build", Resources: wingwire.HostResources{Cores: 1.4}},
			{Pipeline: "owner", ConnectionOnly: true},
			{Pipeline: "waiting-parent", AdmissionWaiting: true},
		},
		Resources:       []wingwire.ResourceState{{Key: "cores", Capacity: 12, Held: 6.6, Reserved: 1, External: 3.3, ExternalSource: wingwire.ExternalMeasured, Available: 1.1}},
		Waiters:         []wingwire.Waiter{{Position: 1, Resources: wingwire.HostResources{Cores: 8}, CostSource: "pin", WaitingOn: []string{"cores"}}},
		ExpectedClearMS: &clear,
	}
	want := "admission: 2 running (pre-push 5.20 cores, docs-build 1.40 cores), 1 queued; you are next - needs 8 cores (explicit pin), 1.10 free (capacity 12, held 6.60, reserved 1, external 3.30); waiting on cores; expected clear ~1m40s"
	if got := AdmissionWaitLine(state, state.Waiters[0]); got != want {
		t.Fatalf("wait line:\n%s\nwant:\n%s", got, want)
	}
	state.Waiters[0].Position = 3
	state.Waiters = append(state.Waiters, wingwire.Waiter{}, wingwire.Waiter{})
	state.ExpectedClearMS = nil
	state.Resources[0].ExternalSource = wingwire.ExternalUnmeasured
	state.Waiters[0].Resources.MemoryBytes = 2 << 30
	state.Resources = append(state.Resources, wingwire.ResourceState{Key: "memory", Capacity: 8 << 30, Held: 4 << 30, ExternalSource: wingwire.ExternalUnattributed, External: 1 << 30, Available: 3 << 30})
	got := AdmissionWaitLine(state, state.Waiters[0])
	for _, part := range []string{"position 3 of 3", "external unmeasured", "needs 2.0 GiB memory", "external 1.0 GiB (unattributed)"} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %q: %s", part, got)
		}
	}
	if strings.Contains(got, "expected clear") || strings.Contains(got, "you are next") {
		t.Fatal(got)
	}
	state.IgnoreExternal = true
	if got := AdmissionWaitLine(state, state.Waiters[0]); !strings.Contains(got, "external ignored") {
		t.Fatal(got)
	}
}
