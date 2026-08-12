package jobs

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestFleetPushChecksDispatchesSparkwingPrePush(t *testing.T) {
	t.Parallel()
	compat, ok := sparkwing.Lookup("push-checks")
	if !ok {
		t.Fatal("push-checks is not registered")
	}
	canonical, ok := sparkwing.Lookup("pre-push")
	if !ok {
		t.Fatal("pre-push is not registered")
	}
	rc := sparkwing.RunContext{Pipeline: "pre-push"}
	compatPlan, err := compat.Invoke(context.Background(), nil, rc)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPlan, err := canonical.Invoke(context.Background(), nil, rc)
	if err != nil {
		t.Fatal(err)
	}
	compatIDs := nodeIDs(compatPlan.Nodes())
	canonicalIDs := nodeIDs(canonicalPlan.Nodes())
	if !reflect.DeepEqual(compatIDs, canonicalIDs) {
		t.Fatalf("push-checks jobs = %v, pre-push jobs = %v", compatIDs, canonicalIDs)
	}
}

func nodeIDs(nodes []*sparkwing.JobNode) []string {
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID()
	}
	return ids
}
