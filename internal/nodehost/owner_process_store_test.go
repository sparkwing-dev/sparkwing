package nodehost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestNativeOwnerProcessDelegatesThroughPrivateTransport(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	owner, err := f.Store.AsOperator().ResolveAuthority(t.Context(), f.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	handler := controller.New(f.Store, nil).Handler()
	host, err := client.NewLocalHostClient(client.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: ownerProcessTransport{handler}}, Bearer: func(context.Context) (string, error) { return string(f.OwnerBearer), nil }}, f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	registry, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: host, Bindings: host})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	masker := secrets.NewMasker()
	processes := NativeProcesses{Authorities: registry, Masks: NewMaskRegistry(masker), URL: "http://process"}
	spec := capability.DelegateSpec{Parent: owner.Authority, Audience: capability.NodeHost, Binding: owner.Capability.Binding, Grants: []capability.Grant{capability.NodeReport}, Repositories: owner.Capability.Repositories, ExpiresAt: owner.Capability.ExpiresAt}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: owner.Capability.Digest}
	if _, err := processes.Delegate(t.Context(), lookup, spec, ProcessServices{SourceBindings: host, RunID: f.Input.RunID, Job: nodeproto.JobSpec{ID: "foreign"}}); err == nil {
		t.Fatal("unpersisted source delegated")
	}
	grant, err := processes.Delegate(t.Context(), lookup, spec, ProcessServices{SourceBindings: host, RunID: f.Input.RunID, Job: nodeproto.JobSpec{ID: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	defer grant.Revoke()
	if grant.Binding.JobID != "build" || grant.Binding.RunID != f.Input.RunID {
		t.Fatal("source identity lost")
	}
	router := routepolicy.New(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(registry), Bindings: registry})
	if err := RegisterProcessServices(router, registry.Resolve); err != nil {
		t.Fatal(err)
	}
	call := func(value string) int {
		r := httptest.NewRequest(http.MethodPost, "/node/v1/masks", strings.NewReader(`{"value":"`+value+`"}`))
		r.Header.Set("Authorization", "Bearer "+grant.Binding.Routes.Bearer)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusOK && w.Code != http.StatusUnauthorized {
			t.Fatalf("process response: %d %s", w.Code, w.Body)
		}
		return w.Code
	}
	if status := call("owner-mask"); status != http.StatusOK || grant.masks.mask("owner-mask") != "***" || masker.Mask("owner-mask") != "owner-mask" {
		t.Fatal("owner process service unavailable")
	}
	row, err := f.Tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "build"})
	if err != nil || row.Attempt != 0 || row.CapabilityPrefix != "" || row.ClaimedBy != "" {
		t.Fatalf("evaluation created work: %+v %v", row, err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "host-fixture-root"}
	_, admin, err := teststore.ProvisionCredential(t.Context(), f.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "admin"}, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: admin.Prefix}, CapabilityPrefix: admin.Prefix}
	if err := f.Tenant.RevokeAuthority(t.Context(), actor, ref); err != nil {
		t.Fatal(err)
	}
	if status := call("after-revoke"); status != http.StatusUnauthorized || masker.Mask("after-revoke") != "after-revoke" {
		t.Fatal("revoked owner retained process services")
	}
}

type ownerProcessTransport struct{ handler http.Handler }

func (p ownerProcessTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	p.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
