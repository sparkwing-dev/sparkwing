package store

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestDeclaredPipelineReferenceProperty(t *testing.T) {
	s, demand := unitClaimFixture(t)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(91, 310))
	var pipeline string
	for range 64 {
		pipeline = fmt.Sprintf("producer-%x", random.Uint64())
		spec, err := json.Marshal(nodeproto.JobSpec{ID: "job", References: []nodeproto.PipelineReference{{Pipeline: pipeline, Job: "output"}}})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, spec)
		target := capability.Target{Team: "a", Reference: &capability.PipelineReferenceTarget{Pipeline: pipeline}}
		if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, target); err != nil {
			t.Fatalf("declared reference refused: %v", err)
		}
		for _, mutation := range []string{"undeclared", "team", "run", "node", "generation"} {
			forged := target
			ref := *target.Reference
			forged.Reference = &ref
			switch mutation {
			case "undeclared":
				forged.Reference.Pipeline += "-other"
			case "team":
				forged.Team = "b"
			case "run":
				forged.RunID = "run"
			case "node":
				forged.NodeID = "job"
			case "generation":
				forged.SourceGeneration = 1
			}
			if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, forged); err == nil {
				t.Fatalf("%s widened the reference", mutation)
			}
		}
	}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
	if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindReadSet, capability.Target{Team: "a", Reference: &capability.PipelineReferenceTarget{Pipeline: pipeline}}); err == nil {
		t.Fatal("replacement job retained old reference authority")
	}
}
