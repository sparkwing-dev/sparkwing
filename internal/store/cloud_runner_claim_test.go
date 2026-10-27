package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// `pipeline trigger --runner cloud` seals the class=cloud runner label; the
// Cloud launcher must claim that work from its queue.
func TestCloudLauncherClaimsWorkSelectingTheCloudClass(t *testing.T) {
	for name, selection := range map[string]runspec.RunnerSelection{
		"cloud class": {Kind: runspec.RunnerLabel, Value: "class=cloud"},
		"runner name": {Kind: runspec.RunnerName, Value: "cloud"},
	} {
		t.Run(name, func(t *testing.T) {
			s, spec := provisionOrigin(t, capability.ProvisionService)
			s, demand := unitClaimFixtureStore(t, s)
			launcher, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			fundUnitClaim(t, s)
			request := runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}}
			request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: selection}}
			rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, sealWith(t, request, assertedProvenance(t, "ci", "", "")))
			demand.CapabilityPrefix, demand.Target, demand.Kinds = launcher.Capability.Prefix, nil, []capability.UnitKind{capability.UnitNode}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if claimed := err == nil && len(claim.Bearer) != 0; claimed != (selection.Kind == runspec.RunnerLabel) {
				t.Fatalf("Cloud launcher claim of %+v: claimed=%v err=%v", selection, claimed, err)
			}
		})
	}
}
