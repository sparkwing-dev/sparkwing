//go:build unix

package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestEnvelopeRecoveryDeclarations(t *testing.T) {
	for _, shape := range []string{"recover-memo", "recover-skip", "recover-conc"} {
		t.Run(shape, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			if err := (envelopeFixture{}).Plan(t.Context(), plan, envelopeInputs{Shape: shape, Key: "rollback-v1", Marker: t.TempDir()}, sparkwing.RunContext{}); err != nil {
				t.Fatal(err)
			}
			doc := sdkserve.DefaultOperations.Document(plan)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()}); err != nil {
				t.Fatal(err)
			}
			for _, job := range doc.Jobs {
				if job.ID != "rollback" {
					continue
				}
				if job.OnFailureOf != "deploy" || shape == "recover-memo" && job.Memo == nil || shape == "recover-skip" && len(job.Closures.SkipIf) != 1 || shape == "recover-conc" && len(job.Concurrency) != 1 {
					t.Fatalf("recovery modifiers missing: %+v", job)
				}
				return
			}
			t.Fatalf("recovery missing: %+v", doc)
		})
	}
}
