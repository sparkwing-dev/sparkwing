package controller

import (
	"crypto/sha256"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// The Cloud launcher never claims a whole-run trigger, so `--runner cloud` must
// leave plan units claimable by it.
func TestTriggerRunnerCloudReachesTheCloudLauncher(t *testing.T) {
	for name, flags := range map[string]runspec.Options{
		"no runner flag": {},
		"--runner cloud": {Runner: runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerLabel, Value: "class=cloud"}}}},
	} {
		t.Run(name, func(t *testing.T) { triggerReachesCloudLauncher(t, flags) })
	}
}

func triggerReachesCloudLauncher(t *testing.T, flags runspec.Options) {
	s, options, input, _ := sourceUploadFixture(t)
	resolved, err := settings.ResolveRequest(settings.Inputs{Context: settings.Context{Origin: runspec.OriginTrigger, Submitter: true, NumCPU: 4}, Flags: settings.Values{Options: flags}})
	if err != nil {
		t.Fatal(err)
	}
	input.Dispatch, input.Options = "", resolved
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{IdempotencyKey: "runner-cloud", Request: input})
	if err != nil {
		t.Fatal(err)
	}
	raw := reviewSeed(t, s.store)
	root, err := s.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := s.store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionService, Authority: &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix},
		RegistrationID: "service:" + root.Capability.Prefix, Subject: capability.Subject{Kind: capability.SubjectService, ID: root.Capability.Prefix},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch},
	})
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "runner-cloud", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	claim, err := s.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: launcher.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}})
	if err != nil || claim.RunID != created.RunID || claim.Kind != capability.UnitPlan {
		t.Fatalf("Cloud launcher poll: %+v %v", claim, err)
	}
}
