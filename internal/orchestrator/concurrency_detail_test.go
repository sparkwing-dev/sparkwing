package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRenderNodes_SurfacesStatusDetail(t *testing.T) {
	var buf bytes.Buffer
	nodes := []*store.Node{
		{NodeID: "deploy", Status: "running", StatusDetail: "queued in deploy-prod: 2 ahead, held by r1/n1"},
	}
	renderNodesWithSteps(&buf, nodes, nil, false)
	if !strings.Contains(buf.String(), "queued in deploy-prod: 2 ahead, held by r1/n1") {
		t.Fatalf("runs status did not surface status_detail:\n%s", buf.String())
	}
}
