package nodehost

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestProcessAuthority(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}
	binding := capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "holder", Generation: 3, Attempt: 2}
	parentPrincipal := capability.Principal{Authority: ref, Class: capability.CredentialBearer, Capability: capability.Capability{Prefix: ref.ID, Digest: sha256.Sum256([]byte("controller-parent")), Audience: capability.Controller, Team: "team", Binding: binding, Grants: []capability.Grant{capability.NodeReport}, ExpiresAt: &expires}}
	parent := &processParent{snapshot: capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: parentPrincipal, MembershipValid: true}}, Binding: capability.BindingState{Current: binding, LeaseExpiresAt: expires}}}
	validateCalls := 0
	authorities, err := NewProcessAuthorities(ProcessAuthorityDependencies{
		Parent: parent, Bindings: processBindingFunc(func(_ context.Context, principal capability.Principal, _ capability.BindingRule, target capability.Target) error {
			if principal.Authority != ref || principal.Capability.Audience != capability.Controller || target.RunID != "run" || target.Team != "team" {
				return errors.New("persisted relation lost its parent or process target")
			}
			return nil
		}),
		ValidateAuthority: func(chain []capability.AuthorityState, _ time.Time) (capability.Principal, error) {
			for _, state := range chain {
				if !state.MembershipValid {
					return capability.Principal{}, errors.New("parent membership ended")
				}
			}
			return chain[0].Principal, nil
		},
		ValidateDelegation: func(p capability.Principal, spec capability.DelegateSpec, at time.Time) error {
			validateCalls++
			if p.Authority != ref || spec.Parent != ref || !reflect.DeepEqual(spec.Binding, binding) || at != now {
				return errors.New("shared delegation validator received incorrect facts")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Digest: parentPrincipal.Capability.Digest, Audience: capability.Controller}
	spec := capability.DelegateSpec{Parent: ref, Audience: capability.NodeHost, Grants: []capability.Grant{capability.NodeReport}, Binding: binding, ExpiresAt: &expires}
	masker := secrets.NewMasker()
	writes := 0
	service := &ProcessServices{RunID: "run", Job: nodeproto.JobSpec{ID: "job"}, Masks: NewMaskRegistry(masker), Summary: func(context.Context, capability.Authorized, nodeproto.SummaryRequest) error { writes++; return nil }}
	issued, revoke, err := authorities.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	if validateCalls != 1 || masker.Mask(issued.Raw) != "***" {
		t.Fatal("process bearer bypassed shared validation or mask registration")
	}
	processLookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: sha256.Sum256([]byte(issued.Raw))}
	snapshot, err := authorities.ReadAuthentication(t.Context(), processLookup, capability.RequestEvidence{})
	if err != nil || len(snapshot.Authorities) != 2 || !snapshot.Binding.ProcessAlive || !reflect.DeepEqual(snapshot.Binding.Current, binding) || *snapshot.Authorities[0].Principal.Capability.Parent != ref {
		t.Fatalf("process snapshot=%+v error=%v", snapshot, err)
	}
	auth := capability.Authorized{Principal: snapshot.Authorities[0].Principal, Binding: binding}
	routes := &processServiceRoutes{endpoints: map[string]controller.Endpoint{}}
	if err := RegisterProcessServices(routes, authorities.Resolve); err != nil {
		t.Fatal(err)
	}
	endpoint := routes.endpoints["/node/v1/summary"]
	call := func(authorized capability.Authorized) error {
		request := httptest.NewRequest(http.MethodPost, "/node/v1/summary", strings.NewReader(`{"markdown":"result"}`))
		_, err := endpoint.Decode(request)
		if err != nil {
			return err
		}
		return endpoint.Handle(httptest.NewRecorder(), request, authorized)
	}
	if err := authorities.CheckBinding(t.Context(), auth.Principal, capability.BindOwnNode, capability.Target{}); err != nil {
		t.Fatal(err)
	}
	if err := call(auth); err != nil || writes != 1 {
		t.Fatalf("registered process service writes=%d error=%v", writes, err)
	}
	foreign := auth
	foreign.Binding.Attempt++
	if err := call(foreign); err == nil || writes != 1 {
		t.Fatal("another attempt reached the process service")
	}
	parent.snapshot.Authorities[0].MembershipValid = false
	snapshot, err = authorities.ReadAuthentication(t.Context(), processLookup, capability.RequestEvidence{})
	if err != nil || snapshot.Authorities[0].MembershipValid || snapshot.Authorities[1].MembershipValid {
		t.Fatal("process snapshot hid parent membership loss")
	}
	if err := authorities.CheckBinding(t.Context(), auth.Principal, capability.BindOwnNode, capability.Target{}); err == nil {
		t.Fatal("ended parent reached a relation check")
	}
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := authorities.ReadAuthentication(t.Context(), processLookup, capability.RequestEvidence{}); err == nil {
		t.Fatal("revoked process remained in authentication snapshots")
	}
	if err := call(auth); err == nil || writes != 1 {
		t.Fatal("revoked process reached its service")
	}
	for _, mode := range []string{"success", "retry", "binding_refused", "bind_error", "start_error"} {
		t.Run("executor_"+mode, func(t *testing.T) {
			parent.snapshot.Authorities[0].MembershipValid = true
			var trace []string
			var processes []*scriptedSDK
			var issued []capability.Issued
			input, _, host := executionHostFixture(t, mode, &trace, &processes, func(deps *ExecutorDependencies) {
				deps.Bind = func(ctx context.Context, in NodeInput) (ProcessGrant, error) {
					binding = capability.Binding{Kind: capability.BindingNode, RunID: in.Accepted.RunID, NodeID: in.NodeID, Holder: in.Authority.Holder, Generation: in.Generation, Attempt: in.Attempt}
					parent.snapshot.Authorities[0].Principal.Capability.Binding = binding
					parent.snapshot.Binding.Current = binding
					spec.Binding, service.Job = binding, in.Accepted.Plan.Jobs[0]
					child, revoke, err := authorities.Delegate(ctx, lookup, spec, service)
					if err != nil {
						return ProcessGrant{}, err
					}
					issued = append(issued, child)
					grant := ProcessGrant{
						Binding: nodeproto.ProcessBinding{RunID: in.Accepted.RunID, JobID: in.NodeID, Routes: nodeproto.ProcessRoutes{URL: "http://host", Bearer: child.Raw}},
						Revoke: func() error {
							for _, process := range processes {
								if process.input.Attempt == in.Attempt && (process.closed || process.cleaned) {
									t.Error("bearer retained beyond process join or cleanup")
								}
							}
							return revoke()
						},
					}
					if mode == "binding_refused" {
						grant.Binding.JobID = "foreign"
					}
					if mode == "bind_error" {
						return grant, errors.New("binding failed after delegation")
					}
					return grant, nil
				}
				start := deps.Start
				deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
					processLookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: sha256.Sum256([]byte(in.RouteBinding.Routes.Bearer))}
					if _, err := authorities.ReadAuthentication(ctx, processLookup, capability.RequestEvidence{}); err != nil {
						return nil, err
					}
					if mode == "start_error" {
						return nil, errors.New("spawn failed")
					}
					return start(ctx, in, activity)
				}
			})
			err := host.Execute(t.Context(), input)
			if (err == nil) != (mode == "success" || mode == "retry") {
				t.Fatalf("execution=%v trace=%v", err, trace)
			}
			want := 1
			if mode == "retry" {
				want = 3
			}
			if len(issued) != want {
				t.Fatalf("delegations=%d want=%d", len(issued), want)
			}
			for _, child := range issued {
				processLookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: sha256.Sum256([]byte(child.Raw))}
				if _, err := authorities.ReadAuthentication(t.Context(), processLookup, capability.RequestEvidence{}); err == nil {
					t.Fatal("executor returned with a live process bearer")
				}
				if masker.Mask(child.Raw) != "***" {
					t.Fatal("process bearer bypassed the shared masker")
				}
			}
		})
	}
}
