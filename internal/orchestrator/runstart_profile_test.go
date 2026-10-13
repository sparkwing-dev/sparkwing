package orchestrator

import (
	"testing"
)

func TestBuildRunInvocation_NoProfileOmitsBlocks(t *testing.T) {
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", "", nil)
	if _, ok := inv["profile"]; ok {
		t.Errorf("profile block must be omitted from a local run; got %v", inv["profile"])
	}
	if _, ok := inv["backends"]; ok {
		t.Errorf("backends block must be omitted from a local run; got %v", inv["backends"])
	}
}
