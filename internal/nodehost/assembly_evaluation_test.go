package nodehost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestEvaluationBindingsSeparateApprovalDependenciesFromGeneratorSource(t *testing.T) {
	for _, unit := range []capability.UnitKind{capability.UnitTrigger, capability.UnitEval} {
		for _, approval := range []bool{false, true} {
			t.Run(string(unit)+map[bool]string{false: "/generator", true: "/approval"}[approval], func(t *testing.T) {
				source := nodeproto.JobSpec{ID: "gate", Needs: []string{"build"}, References: []nodeproto.PipelineReference{{Pipeline: "upstream", Job: "artifact"}}}
				if approval {
					source.Approval = &nodeproto.ApprovalSpec{}
					source.Closures.SkipIf = []string{"guard"}
				}
				body, err := json.Marshal(source)
				if err != nil {
					t.Fatal(err)
				}
				row := runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: "gate"}, Generation: 4, SpecHash: "hash", Spec: body}
				binding := evaluationBindings{source: row.Ref, generation: 4, hash: row.SpecHash, readNode: func(_ context.Context, ref runspec.NodeRef) (runstate.NodeRow, error) {
					if ref.NodeID == "gate" {
						return row, nil
					}
					body, err := json.Marshal(nodeproto.JobSpec{ID: string(ref.NodeID)})
					return runstate.NodeRow{Ref: ref, Spec: body}, err
				}}
				p := capability.Principal{Capability: capability.Capability{Unit: unit, Binding: capability.Binding{RunID: "run"}}}
				for _, target := range []capability.Target{{RunID: "run", NodeID: "build"}, {Reference: &capability.PipelineReferenceTarget{Pipeline: "upstream", JobID: "artifact"}}} {
					err := binding.CheckBinding(t.Context(), p, capability.BindReadSet, target)
					allowed := approval || unit == capability.UnitTrigger
					if (err == nil) != allowed {
						t.Fatalf("target=%+v allowed=%v error=%v", target, allowed, err)
					}
				}
				if err := binding.CheckBinding(t.Context(), p, capability.BindReadSet, capability.Target{RunID: "run", NodeID: "undeclared"}); err == nil {
					t.Fatal("read undeclared output")
				}
				row.Generation = runspec.ClaimGeneration(5)
				if err := binding.CheckBinding(t.Context(), p, capability.BindReadSet, capability.Target{RunID: "run", NodeID: "build"}); err == nil {
					t.Fatal("read under changed evaluation source fence")
				}
			})
		}
	}
}
