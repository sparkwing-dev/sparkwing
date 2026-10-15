package nodehost

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestProcessAuthorityRoutesUsePersistedParentRelations(t *testing.T) {
	job := nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
	ref := store.NodeRef{RunID: f.Input.RunID, NodeID: job.ID}
	if _, err := f.Tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, To: store.NodeReady, Actor: f.Actor, Cause: store.CauseRelease}); err != nil {
		t.Fatal(err)
	}
	award, err := f.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceNone})
	if err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.Node.CapabilityPrefix}, CapabilityPrefix: award.Node.CapabilityPrefix, Holder: award.Node.ClaimedBy, Generation: award.Node.Generation}
	if _, err := f.Tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, To: store.NodeRunning, Actor: actor, Cause: store.CauseStart, SourceAttempt: &award.Node.Attempt}); err != nil {
		t.Fatal(err)
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256(award.Bearer)}
	snapshot, err := f.Store.ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	parent := snapshot.Authorities[0].Principal
	registry, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: f.Store, Bindings: f.Store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	service := &ProcessServices{RunID: ref.RunID, Job: job, Masks: NewMaskRegistry(secrets.NewMasker())}
	issued, revoke, err := registry.Delegate(t.Context(), lookup, capability.DelegateSpec{Parent: parent.Authority, Audience: capability.NodeHost, Grants: []capability.Grant{capability.NodeReport}, Binding: snapshot.Binding.Current, ExpiresAt: parent.Capability.ExpiresAt}, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(registry), Bindings: registry})
	if err := RegisterProcessServices(router, registry.Resolve); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/node/v1/masks", strings.NewReader(`{"value":"private-value"}`))
	request.Header.Set("Authorization", "Bearer "+issued.Raw)
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, request)
	if reply.Code != http.StatusOK || service.Masks.mask("private-value") != "***" {
		t.Fatalf("live process route: status=%d body=%s", reply.Code, reply.Body)
	}
	check := func(run, node, team, fence uint8) bool {
		selector := func(value string, change uint8) string {
			if change%2 == 0 {
				return value
			}
			return fmt.Sprintf("%s-%d", value, change)
		}
		principal := processAuthorized(issued).Principal
		principal.Capability.Binding.Attempt += int64(fence % 2)
		target := capability.Target{RunID: selector(ref.RunID, run), NodeID: selector(ref.NodeID, node), Team: selector(string(store.DefaultTeam), team)}
		err := registry.CheckBinding(t.Context(), principal, capability.BindOwnNode, target)
		return (err == nil) == (run%2 == 0 && node%2 == 0 && team%2 == 0 && fence%2 == 0)
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, To: store.NodeSuccess, Actor: actor, Cause: store.CauseReport, SourceAttempt: &issued.Capability.Binding.Attempt, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeSuccess}}}); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/node/v1/masks", strings.NewReader(`{"value":"ended-value"}`))
	request.Header.Set("Authorization", "Bearer "+issued.Raw)
	reply = httptest.NewRecorder()
	router.ServeHTTP(reply, request)
	if reply.Code != http.StatusUnauthorized || service.Masks.mask("ended-value") != "ended-value" {
		t.Fatalf("ended process route: status=%d body=%s", reply.Code, reply.Body)
	}
}
