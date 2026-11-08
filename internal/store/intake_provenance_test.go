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

func TestDetachedChildInheritsVerifiedDefaultRepositoryIdentity(t *testing.T) {
	s := authorityStore(t)
	proof, git := referenceAppProvenance(t, "refs/heads/main", referenceMain)
	child := childRequest("deploy", git.Ref, git.SHA)
	child.Lineage.Detached = true
	tenant, id := childReference(t, s, proof, git, child)
	row, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	got := row.Request.Provenance
	selection, err := runSecretSelection(row.Request)
	if err != nil || selection.scope != secretPipelineScope || selection.repo != "gh:1" || selection.pipeline != proof.Pipeline() {
		t.Fatalf("child lost inherited pipeline-secret scope: %+v, %v", selection, err)
	}
	if got.Trust() != runspec.TrustVerifiedDefault || got.ProvenRepository() != "gh:1" || got.SharedSecretsOnly() || !row.Lineage.Detached {
		t.Fatalf("detached child lost verified-default runner/secret eligibility: %+v, %+v", got, row.Lineage)
	}
}

func TestDetachedChildDoesNotUpgradeAssertedParent(t *testing.T) {
	tenant, request := childIntakeFixture(t)
	request.Request.Lineage.Detached = true
	result, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadRun(t.Context(), result.RunID)
	if err != nil || row.Request.Provenance.Trust() != runspec.TrustAsserted || row.Request.Provenance.ProvenRepository() != "" {
		t.Fatalf("detaching upgraded an asserted parent: %v, %v", row.Request.Provenance.Trust(), err)
	}
}
