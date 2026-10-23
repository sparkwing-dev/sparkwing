package store

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestGeneratedSpecReferencesAreSealedWithTheAcceptedDeclarationProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(871, 197))
	for range 16 {
		tenant, request := successfulExpansionFixture(t)
		id := fmt.Sprintf("generated-%x", random.Uint64())
		refs := []nodeproto.PipelineReference{{Pipeline: fmt.Sprintf("pipeline-%x", random.Uint64()), Job: "result"}}
		job := nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "step"}}, Groups: []string{"group"}, Needs: []string{"job"}, References: refs}
		body, err := json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
		if err != nil {
			t.Fatal(err)
		}
		request.Body = body
		accepted, err := tenant.AcceptExpansion(t.Context(), request)
		if err != nil || len(accepted.Nodes) != 1 {
			t.Fatalf("expansion: %+v, %v", accepted, err)
		}
		row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: request.Source.RunID, NodeID: runspec.NodeID(id)})
		if err != nil {
			t.Fatal(err)
		}
		hash, err := runspec.CanonicalHash(job)
		if err != nil || row.SpecHash != string(hash) {
			t.Fatalf("generated spec hash=%q want %q, %v", row.SpecHash, hash, err)
		}
		var persisted nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &persisted); err != nil {
			t.Fatal(err)
		}
		if len(persisted.References) != 1 || persisted.References[0] != refs[0] {
			t.Fatalf("persisted references=%+v", persisted.References)
		}
		job.References[0].Pipeline = "undeclared"
		body, err = json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
		if err != nil {
			t.Fatal(err)
		}
		request.Body = body
		if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
			t.Fatal("replay replaced declared reference")
		}
		after, err := tenant.ReadNode(t.Context(), row.Ref)
		if err != nil || string(after.Spec) != string(row.Spec) || after.SpecHash != row.SpecHash {
			t.Fatal("refused reference change reached generated spec")
		}
	}
}
