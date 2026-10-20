package nodehost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestResumeRefusesARowPlannedUnderAnotherSpec(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "held", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "lost", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "build", Optional: true, Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	claimedFixtureNode(t, f, "held")
	if err := f.report(t.Context(), claimedFixtureNode(t, f, "lost"), store.NodeFailed); err != nil {
		t.Fatal(err)
	}
	before, err := f.Tenant.ReadSnapshot(t.Context(), f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	input := f.Input
	accepted := *input.Accepted
	body, err := json.Marshal(accepted.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &accepted.Plan); err != nil {
		t.Fatal(err)
	}
	accepted.Plan.Jobs[2].Optional = false
	input.Accepted = &accepted
	f.Host.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
		t.Fatal("drifted resume acquired source")
		return nil, nil
	}
	if _, err := f.Host.Run(t.Context(), input); err == nil || !strings.Contains(err.Error(), "resume differs from persisted acceptance") {
		t.Fatalf("resume accepted another spec: %v", err)
	}
	after, err := f.Tenant.ReadSnapshot(t.Context(), f.Input.RunID)
	if err != nil || len(after.Nodes) != len(before.Nodes) {
		t.Fatalf("read refused resume: %v", err)
	}
	for i, row := range before.Nodes {
		got := after.Nodes[i]
		if got.Ref != row.Ref || got.Status != row.Status || got.Outcome != row.Outcome || got.ClaimedBy != row.ClaimedBy || got.Generation != row.Generation || got.SpecHash != row.SpecHash {
			t.Fatalf("refused resume changed held or terminal row: before=%+v after=%+v", row, got)
		}
	}
}
