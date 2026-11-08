package store

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRunViewExplainsMachineAdmissionUntilGrant(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',started_at=NULL WHERE team='a' AND run_id='run'`)
	for _, kind := range []string{runstate.EventAdmissionQueued, runstate.EventAdmissionGranted} {
		if err := tenant.AppendEvent(t.Context(), "run", "job", kind, []byte(`{"blocking_reason":"needs 2 cores, 1 available","position":2}`)); err != nil {
			t.Fatal(err)
		}
		view, err := tenant.ReadRunView(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		detail := view.Snapshot.Nodes[0].StatusDetail
		if strings.Contains(detail, "needs 2 cores, 1 available") != (kind == runstate.EventAdmissionQueued) {
			t.Fatalf("%s status: %q", kind, detail)
		}
	}
}
