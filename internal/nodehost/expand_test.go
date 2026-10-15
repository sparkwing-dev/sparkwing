package nodehost

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

const testGenerator = "targets/generate/0"

func fanOutDoc() nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "targets", Closures: nodeproto.ClosureSpec{Generate: []string{testGenerator}}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "summary", Needs: deps("lint"), NeedsGenerated: []string{testGenerator}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "lint", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}
}

func TestValidateNodesChecksGeneratedEdges(t *testing.T) {
	doc := fanOutDoc()
	doc.Jobs[1].NeedsGenerated = []string{"elsewhere/generate/0"}
	if err := validateNodes(doc.Jobs, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err == nil || !strings.Contains(err.Error(), "generator is absent or self-referential") {
		t.Errorf("unknown generator: err = %v", err)
	}
	doc = fanOutDoc()
	doc.Jobs[0].Needs = deps("summary")
	if err := validateNodes(doc.Jobs, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Errorf("a source that needs its own waiter: err = %v", err)
	}
}
