package nodeproto

import (
	"encoding/json"
	"testing"
)

func TestPlanDocWireKeys(t *testing.T) {
	raw, err := json.Marshal(PlanDoc{Protocol: "1", Pipeline: "demo", RunID: "run-1", Nodes: []PlanNode{}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["protocol"] != "1" {
		t.Errorf("protocol = %v, want \"1\"; raw %s", got["protocol"], raw)
	}
	if nodes, ok := got["nodes"].([]any); !ok || len(nodes) != 0 {
		t.Errorf("nodes = %v, want an empty array; raw %s", got["nodes"], raw)
	}
}
