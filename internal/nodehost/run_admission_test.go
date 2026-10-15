package nodehost

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func (d *fakeDaemon) lease(runID string) *fakeLease {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.leases[runID]
}

func planGrouped(scope, onLimit string, nodes ...string) nodeproto.PlanDoc {
	concurrency := nodeproto.ConcurrencySpec{Group: "deploys", Scope: new(scope), Capacity: new(1)}
	if onLimit != "" {
		concurrency.OnLimit = new(onLimit)
	}
	doc := nodeproto.PlanDoc{Concurrency: []nodeproto.ConcurrencySpec{concurrency}}
	for _, id := range nodes {
		doc.Jobs = append(doc.Jobs, nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "body"}}})
	}
	return doc
}

var boxKey = scopedConcurrencyKey("deploys", "box", "", testBox)

func TestPlanGroupFixtureDeclaresScopeWithoutHostKey(t *testing.T) {
	raw, err := json.Marshal(planGrouped("box", "queue", "a"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ConcurrencyKey(doc.Concurrency[0], "run-1", testBox); got != "box:5:box-1:deploys" {
		t.Fatalf("group targets %q, want the occupied box:5:box-1:deploys", got)
	}
}
