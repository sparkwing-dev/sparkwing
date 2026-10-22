package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type storedRunnerW8 struct {
	controllerwire.Controller
	server     *storedEngineW8
	authorized capability.Authorized
}

func (c *storedRunnerW8) Claim(ctx context.Context, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.Controller.Claim(ctx, body)
}

func (c *storedRunnerW8) Renew(ctx context.Context, unit string, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	if unit != c.authorized.Principal.Capability.UnitID {
		return controllerwire.RenewalResponse{}, errors.New("renewal selected another unit")
	}
	return c.server.Renew(ctx, c.authorized, body)
}

func (c *storedRunnerW8) Exchange(ctx context.Context) (controllerwire.ExchangeResponse, error) {
	return c.server.Exchange(ctx, c.authorized)
}

func (c *storedRunnerW8) TransitionJob(ctx context.Context, run, job string, body controllerwire.JobTransitionRequest) (controllerwire.Applied, error) {
	authorized := c.authorized
	authorized.Target.RunID, authorized.Target.NodeID = run, job
	return c.server.TransitionJob(ctx, authorized, body)
}

func (c *storedRunnerW8) Finish(ctx context.Context, run string, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
	authorized := c.authorized
	authorized.Target.RunID = run
	return c.server.Finish(ctx, authorized, body)
}

type runnerStorePolicyW8 struct{}

func (runnerStorePolicyW8) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, error) {
	return match.Profile{}, nil
}

func (runnerStorePolicyW8) CheckRunner(_ context.Context, version string) error {
	if version != "v0.71.0" {
		return errors.New("unexpected runner version")
	}
	return nil
}

func newStoredRunnerW8(t *testing.T, kind capability.UnitKind) (*storedEngineW8, capability.Issued) {
	t.Helper()
	s, err := teststore.Open(filepath.Join(t.TempDir(), "runner.db"), runnerStoreOptionsW8())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return seedStoredRunnerW8(t, s, kind)
}

func seedStoredRunnerW8(t *testing.T, s *store.Store, kind capability.UnitKind) (*storedEngineW8, capability.Issued) {
	t.Helper()
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	_, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Seed: raw, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}

	parent := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	root, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, RegistrationID: "runner:default:runner", Repositories: []string{"github.com/team/repo"}, Authority: &parent, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}})
	if err != nil {
		t.Fatalf("fixture machine: %v", err)
	}
	unit := runnerAuthorityAwardW8(capability.UnitNode)
	unit.Request.Request.Team = string(store.DefaultTeam)
	unit.Request.Request.Git.Repository.Key = "gh:1"
	unit.Request.Request.Git.Repository.Display = "https://github.com/team/repo"
	unit.Request.Request.Git.SHA = strings.Repeat("a", 40)
	unit.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://github.com/team/repo.git", PipelineDir: ".sparkwing"}
	unit.Request.Hash = "request"
	provenance, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "fixture"), "", unit.Request.Request.Git.SHA, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	unit.Request.Provenance = provenance
	unit.Accepted.Spec.Request = unit.Request
	unit.Accepted.Spec.Hash = "spec"
	request, err := json.Marshal(unit.Request)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(unit.Accepted.Spec)
	if err != nil {
		t.Fatal(err)
	}
	plan := []byte(`{"jobs":[]}`)
	if kind == capability.UnitNode {
		plan = []byte(`{"jobs":[{"id":"job"}]}`)
	}
	now := teststore.Now(s)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,spec_json,spec_hash,sealed_at,plan_json,plan_hash)
VALUES (?,'run',1,'fixture','running',?,'request',?,'gh:1','https://github.com/team/repo','host','execute',?,'fixture',?,'r30',30,?,'spec',?,?,'plan')`, []any{store.DefaultTeam, request, now.UnixNano(), []byte(`[]`), now.UnixNano(), spec, now.UnixNano(), plan}},
		{`INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES (?,'run','trigger',?,?,?,?,?)`, []any{store.DefaultTeam, now.UnixNano(), now.UnixNano(), now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`)}},
	} {
		if _, err := teststore.Exec(t.Context(), s, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if kind == capability.UnitNode {
		if _, err := teststore.SQL(t, s).ExecContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES (?,'run','job','pending',?,?,?)`, store.DefaultTeam, now.UnixNano(), []byte(`{"id":"job"}`), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	server := newStoredEngineW8(s, controller.EngineController{RunnerPolicy: runnerStorePolicyW8{}})
	server.Bootstrap = parent
	return server, root
}

func storedRunnerClientW8(ctx context.Context, server *storedEngineW8, bearer string) (*storedRunnerW8, error) {
	principal, err := server.Store.AsOperator().LookupCapability(ctx, sha256.Sum256([]byte(bearer)))
	if err != nil {
		return nil, err
	}
	c := principal.Capability
	handler := controller.New(server.Store, nil).WithEngineController(server.EngineController).Handler()
	transport, err := client.NewEngineClient(client.EngineOptions{
		URL: "http://controller", Launcher: slices.Contains(c.Grants, capability.ClaimsLaunch),
		Bearer: func(context.Context) (string, error) { return bearer, nil },
		HTTP: &http.Client{Transport: surfaceTransport(func(request *http.Request) (*http.Response, error) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Result(), nil
		})},
	})
	if err != nil {
		return nil, err
	}
	return &storedRunnerW8{Controller: transport, server: server, authorized: capability.Authorized{Principal: principal, Binding: c.Binding, Target: capability.Target{Team: c.Team, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID}}}, nil
}

func revokeStoredRunnerW8(t *testing.T, s *store.Store, target capability.AuthorityRef) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('runner-admin','runner-admin@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','runner-admin','owner',1)`,
		`INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('runner-admin','runner-admin','runs.read,team.admin',1,4102444800000000000,'default','runner-admin')`,
		`INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('runner-admin','default',0)`,
	} {
		if _, err := teststore.SQL(t, s).ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "runner-admin"}}, target); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStoreRevocationPreventsPreparationW8Components(t *testing.T) {
	server, root := newStoredRunnerW8(t, capability.UnitTrigger)
	claimant, err := storedRunnerClientW8(t.Context(), server, root.Raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := claimant.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}})
	if err != nil || response.Claim == nil {
		t.Fatal(err)
	}
	rootRef := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}
	revokeStoredRunnerW8(t, server.Store, rootRef)
	host := runners.Hosting{ControllerURL: "https://controller.example", NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
		bearer, err := options.Bearer(t.Context())
		if err != nil {
			return nil, err
		}
		return storedRunnerClientW8(t.Context(), server, bearer)
	}, Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (runners.NativeServices, capability.Principal, error) {
		t.Fatal("revoked award reached native binding")
		return runners.NativeServices{}, capability.Principal{}, nil
	}}
	if err := host.Host(t.Context(), *response.Claim); err == nil {
		t.Fatal("revoked root reached hosting")
	}
}

func runnerStoreOptionsW8() store.Option {
	return store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{ClaimProfiles: runnerStorePolicyW8{}}, nil
	})
}

type storedEngineW8 struct {
	*controller.EngineController
	Store     *store.Store
	Bootstrap capability.AuthorityRef
}

func newStoredEngineW8(st *store.Store, cfg controller.EngineController) *storedEngineW8 {
	return &storedEngineW8{EngineController: controller.NewEngineController(st, cfg), Store: st}
}
