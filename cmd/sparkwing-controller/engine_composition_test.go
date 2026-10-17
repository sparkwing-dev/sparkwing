package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestControllerCompositionAdmitsLauncherAndRunnerClaims(t *testing.T) {
	root := t.TempDir()
	st, err := openControllerStore(t.Context(), filepath.Join(root, "state.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seed := sha256.Sum256([]byte("bootstrap"))
	admin := "swk_" + base64.RawURLEncoding.EncodeToString(seed[:])
	if _, err := controller.EnsureBootstrapAdminToken(t.Context(), st, admin); err != nil {
		t.Fatal(err)
	}
	srv, err := newControllerServer(st, root, "commit-0123456")
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	bearer := func(raw string) client.EngineOptions {
		return client.EngineOptions{URL: api.URL, Bearer: func(context.Context) (string, error) { return raw, nil }}
	}

	operator, err := client.NewTokenManager(bearer(admin), true)
	if err != nil {
		t.Fatal(err)
	}
	launcherToken, err := operator.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: "service", Principal: "launcher", Scopes: []capability.Grant{capability.ClaimsLaunch}})
	if err != nil || launcherToken.Metadata.Principal != "launcher" {
		t.Fatalf("service token %+v: %v", launcherToken.Metadata, err)
	}
	teams, err := client.NewOperatorManager(bearer(admin))
	if err != nil {
		t.Fatal(err)
	}
	team, err := teams.CreateOperatorTeam(t.Context(), controllerwire.OperatorTeamCreate{Slug: "qa-team"})
	if err != nil {
		t.Fatal(err)
	}
	operatorPrincipal, err := st.LookupCapability(t.Context(), sha256.Sum256([]byte(admin)))
	if err != nil {
		t.Fatal(err)
	}
	runnerToken, err := st.ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, RegistrationID: "runner:qa-team:runner", Authority: &operatorPrincipal.Authority,
		Team: team.Team, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Repositories: []string{"github.com/qa/*"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim},
	})
	if err != nil {
		t.Fatal(err)
	}

	launcherOptions := bearer(launcherToken.Token)
	launcherOptions.Launcher = true
	for name, options := range map[string]client.EngineOptions{"launcher": launcherOptions, "runner": bearer(runnerToken.Raw)} {
		claims, err := client.NewEngineClient(options)
		if err != nil {
			t.Fatal(err)
		}
		response, err := claims.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "commit-0123456", Kinds: []capability.UnitKind{capability.UnitNode}, OS: "linux", Arch: "amd64", AvailableCores: 2, AvailableMemoryBytes: 4 << 30})
		if err != nil || response.Claim != nil {
			t.Fatalf("%s claim: %+v %v", name, response.Claim, err)
		}
	}
}
