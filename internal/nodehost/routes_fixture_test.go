package nodehost

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
)

type routeParent struct {
	mu        sync.Mutex
	snapshots map[[sha256.Size]byte]capability.AuthenticationSnapshot
}

func (p *routeParent) ReadAuthentication(_ context.Context, lookup capability.CredentialLookup, _ capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, ok := p.snapshots[lookup.Digest]
	if !ok || lookup.Class != capability.CredentialBearer || lookup.Audience != capability.Controller {
		return capability.AuthenticationSnapshot{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "unknown parent"}
	}
	return snapshot, nil
}

type routeRelations struct{}

func (routeRelations) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return nil
}

type routeTransport struct{ handler http.Handler }

func (transport routeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

type server struct {
	authorities *ProcessAuthorities
	parent      *routeParent
	handler     http.Handler
	client      *http.Client
}

func newService(t testing.TB) *ProcessServices {
	t.Helper()
	return &ProcessServices{RunID: "run-1", Job: nodeproto.JobSpec{ID: "deploy", Steps: []nodeproto.StepSpec{{ID: "body"}}}, Masks: NewMaskRegistry(secrets.NewMasker())}
}

type routeNode struct {
	RunID, NodeID string
	Job           nodeproto.JobSpec
	Secrets       []string
	Resolver      interface {
		Resolve(context.Context, string) (string, bool, error)
	}
	Masker  *secrets.Masker
	Service *ProcessServices
}

func (s *server) Admit(node routeNode) (string, func(), error) {
	service := node.Service
	if service == nil {
		job := node.Job
		job.ID = node.NodeID
		service = &ProcessServices{RunID: node.RunID, Job: job, Secrets: node.Secrets, Masks: NewMaskRegistry(node.Masker)}
		if node.Resolver != nil {
			service.Secret = func(ctx context.Context, _ capability.Authorized, name string) (string, bool, error) {
				value, masked, err := node.Resolver.Resolve(ctx, name)
				if err == secretsource.ErrMissing {
					err = &apierr.Error{Code: apierr.NotFound, Message: "secret missing"}
				}
				return value, masked, err
			}
		}
	}
	s.parent.mu.Lock()
	id := fmt.Sprintf("parent-%d", len(s.parent.snapshots))
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	binding := capability.Binding{Kind: capability.BindingNode, RunID: service.RunID, NodeID: service.Job.ID, Generation: 1, Attempt: 1, Holder: id}
	grants := []capability.Grant{capability.NodeReport, capability.SecretsRelease, capability.DataRead, capability.RunsRead, capability.RunsWrite, capability.RefsRead, capability.OIDCMint, capability.ChildrenStart}
	principal := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: id}, Capability: capability.Capability{Prefix: id, Digest: sha256.Sum256([]byte(id)), Audience: capability.Controller, Unit: capability.UnitNode, UnitID: id, Purpose: capability.PurposeWork, Issuance: capability.Award, Team: "team", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "machine"}, Binding: binding, Grants: grants, IssuedAt: now, ExpiresAt: &expires}}
	s.parent.snapshots[principal.Capability.Digest] = capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: principal}}, Binding: capability.BindingState{Current: binding, LeaseExpiresAt: expires, StartPermitted: true}}
	s.parent.mu.Unlock()
	issued, revoke, err := s.authorities.delegate(context.Background(), capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: principal.Capability.Digest}, capability.DelegateSpec{Parent: principal.Authority, Audience: capability.NodeHost, Binding: binding, Grants: grants, ExpiresAt: &expires}, service)
	if err != nil {
		return "", nil, err
	}
	return issued.Raw, func() { _ = revoke() }, nil
}
func (s *server) Close() error { return s.authorities.Close() }
func (s *server) URL() string  { return "http://127.0.0.1" }

func start(t testing.TB) *server {
	t.Helper()
	parent := &routeParent{snapshots: map[[sha256.Size]byte]capability.AuthenticationSnapshot{}}
	authorities, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: parent, Bindings: routeRelations{}})
	if err != nil {
		t.Fatal(err)
	}
	router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
	if err := RegisterProcessServices(router, authorities.Resolve); err != nil {
		t.Fatal(err)
	}
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
	s := &server{authorities: authorities, parent: parent, handler: router, client: &http.Client{Transport: routeTransport{handler: router}}}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func admitService(t testing.TB, s *server, service *ProcessServices) string {
	t.Helper()
	bearer, _, err := s.Admit(routeNode{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	return bearer
}
