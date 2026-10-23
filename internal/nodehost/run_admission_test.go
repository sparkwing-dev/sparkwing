package nodehost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
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

func TestPlanGroupFixtureRefusesBoxScope(t *testing.T) {
	raw, err := json.Marshal(planGrouped("box", "queue", "a"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}})
	if err == nil || !strings.Contains(err.Error(), "deploys") || !strings.Contains(err.Error(), "job-level") {
		t.Fatalf("plan box scope refusal: %v", err)
	}
}
