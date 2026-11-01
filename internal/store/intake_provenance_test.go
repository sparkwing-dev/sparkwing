package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestChildSealKeepsLineageWhenCompareIsUnavailable(t *testing.T) {
	s := authorityStore(t)
	proof, git := referenceAppProvenance(t, "refs/heads/main", referenceMain)
	child := childRequest("deploy", "refs/heads/feature", referenceFeature)
	tenant, _ := childReference(t, s, proof, git, child)
	child.Version, child.Team, child.Mode, child.Dispatch = 1, "a", runspec.IntakeExecute, runspec.DispatchHost
	child.Lineage = runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}
	result, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "child-parent"}, IdempotencyKey: "unavailable-child", Request: child, UnverifiedReason: "GitHub compare unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := run.Request.Provenance; got.Trust() != runspec.TrustAsserted || !got.SharedSecretsOnly() || got.Writer() != runspec.RunWriter("run") || got.DataWriter() != runspec.RunWriter("run") || got.UnverifiedReason() != "" {
		t.Fatalf("unavailable compare changed child identity: %+v", got)
	}
}
