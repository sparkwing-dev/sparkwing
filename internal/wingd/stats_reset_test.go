package wingd

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestStatsReset_ClearsTheEventWindow(t *testing.T) {
	d := handlerDaemon(t, 2)
	state := policyRequest(t, d, wingwire.AdmissionRequest{RunID: "stats-run", Resources: wingwire.HostResources{Cores: 1}})
	if state.Grant == nil {
		t.Fatalf("admission: %+v", state)
	}
	before := d.readQueueState()
	if before.Events == nil || before.Events.Runs != 1 || len(before.Holders) != 1 {
		t.Fatalf("grant did not reach event window: %+v", before.Events)
	}
	w := policyHTTP(t, d, http.MethodPost, admissionHTTPPath+"/stats/reset", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: HTTP %d: %s", w.Code, w.Body.String())
	}
	after := d.readQueueState()
	if after.Events != nil || len(after.Holders) != 1 || after.Holders[0].RunID != "stats-run" || after.Holders[0].Resources != before.Holders[0].Resources {
		t.Fatalf("reset changed leases or retained events: %+v", after)
	}
}
