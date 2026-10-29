package nodehost

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func capacityPlan(policy string, timeout int64, ids ...string) nodeproto.PlanDoc {
	doc := nodeproto.PlanDoc{}
	for _, id := range ids {
		doc.Jobs = append(doc.Jobs, nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "body"}}, Concurrency: []nodeproto.ConcurrencySpec{{Group: "db", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new(policy), QueueTimeoutMS: new(timeout)}}})
	}
	return doc
}

func capacityRow(t *testing.T, fixture *storeHostFixture, id string) runstate.NodeRow {
	t.Helper()
	row, err := fixture.Tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(fixture.Accepted.RunID), NodeID: runspec.NodeID(id)})
	if err != nil {
		t.Fatal(err)
	}
	return row
}
