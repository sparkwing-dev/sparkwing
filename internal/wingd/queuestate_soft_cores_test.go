package wingd

import (
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestQueueReasonUsesAdmissionCorePolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		soft      bool
		memory    uint64
		wantCores bool
	}{
		{"soft estimate waits for semaphore", true, 0, false},
		{"hard request waits for cores", false, 0, true},
		{"soft estimate waits for memory", true, 16 << 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newHeadroomDaemon(t, 10, 0)
			claims := []admission.SemaphoreClaim{{Key: "exclusive", Capacity: 1, Cost: 1}}
			if _, _, err := d.ledger.Submit(admission.Request{ID: "holder", Cores: 6, MemoryBytes: tc.memory, Semaphores: claims}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := d.ledger.Submit(admission.Request{ID: "waiter", Cores: 6, SoftCores: tc.soft, MemoryBytes: 1 << 30, Semaphores: claims}); err != nil {
				t.Fatal(err)
			}
			source := wingwire.CostSourcePin
			if tc.soft {
				source = wingwire.CostSourceMeasured
			}
			c := &conn{resources: wingwire.HostResources{Cores: 6, MemoryBytes: 1 << 30}, costSource: string(source)}
			d.byRun["waiter"] = c
			qs := queueState(t, d)
			if len(qs.Waiters) != 1 {
				t.Fatalf("waiters = %d, want 1", len(qs.Waiters))
			}
			waiter := qs.Waiters[0]
			if got := slices.Contains(waiter.WaitingOn, "cores"); got != tc.wantCores {
				t.Errorf("waiting on %v, core blocker = %v, want %v", waiter.WaitingOn, got, tc.wantCores)
			}
			for _, reason := range []string{waiter.BlockingReason, d.hostBlockingReasonLocked(c)} {
				if got := strings.Contains(reason, "cores"); got != tc.wantCores {
					t.Errorf("reason %q, core blocker = %v, want %v", reason, got, tc.wantCores)
				}
				if tc.memory > 0 && !strings.Contains(reason, "GiB") {
					t.Errorf("memory blocker missing: %q", reason)
				}
			}
			if !strings.Contains(waiter.BlockingReason, "exclusive") {
				t.Errorf("semaphore blocker missing: %q", waiter.BlockingReason)
			}
			if waiter.Resources.Cores != 6 {
				t.Errorf("displayed demand = %v, want 6", waiter.Resources.Cores)
			}
		})
	}
}
