package orchestrator

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestRecordRunProfileUsesOriginatingCPUAvailability(t *testing.T) {
	st, start := seedUsageRun(t, "origin", []usageNode{{id: "build", dur: time.Second, samples: ticks(1, 0, 100)}})
	recordRunProfile(t.Context(), localState{st: st}, "origin", "r1", nil, "", runCharge{}, false, start, start.Add(time.Second))
	for _, node := range []string{"", "build"} {
		p, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(t.Context(), "origin", node)
		if err != nil || p == nil || !p.CPUMeasured || p.SampleCount != 1 || p.PeakCores != 0 {
			t.Fatalf("originating measured zero lost for %q: %+v, %v", node, p, err)
		}
	}
}
