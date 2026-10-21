package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func composedClaimants(t *testing.T, version string, policy *http.Client) map[string]controllerwire.Controller {
	t.Helper()
	root := t.TempDir()
	st, err := openControllerStore(t.Context(), deployment.Config{}, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seed := sha256.Sum256([]byte("bootstrap"))
	admin := "swk_" + base64.RawURLEncoding.EncodeToString(seed[:])
	if _, err := controller.EnsureBootstrapAdminToken(t.Context(), st.AsOperator(), admin); err != nil {
		t.Fatal(err)
	}
	srv, err := newControllerServer(st, deployment.Config{}, root, version, policy)
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
	operatorPrincipal, err := st.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(admin)))
	if err != nil {
		t.Fatal(err)
	}
	runnerToken, err := st.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, RegistrationID: "runner:qa-team:runner", Authority: &operatorPrincipal.Authority,
		Team: team.Team, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Repositories: []string{"github.com/qa/*"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim},
	})
	if err != nil {
		t.Fatal(err)
	}
	launcherOptions := bearer(launcherToken.Token)
	launcherOptions.Launcher = true
	claimants := map[string]controllerwire.Controller{}
	for name, options := range map[string]client.EngineOptions{"launcher": launcherOptions, "runner": bearer(runnerToken.Raw)} {
		if claimants[name], err = client.NewEngineClient(options); err != nil {
			t.Fatal(err)
		}
	}
	return claimants
}

func claimAs(t *testing.T, claims controllerwire.Controller, version string) error {
	t.Helper()
	response, err := claims.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: version, Kinds: []capability.UnitKind{capability.UnitNode}, OS: "linux", Arch: "amd64"})
	if err == nil && response.Claim != nil {
		t.Fatalf("idle controller awarded %+v", response.Claim)
	}
	return err
}

func TestControllerCompositionAdmitsLauncherAndRunnerClaims(t *testing.T) {
	for name, claims := range composedClaimants(t, "commit-0123456", nil) {
		if err := claimAs(t, claims, "commit-0123456"); err != nil {
			t.Fatalf("%s claim: %v", name, err)
		}
	}
}

type signedPolicyTransport struct{ body, signature []byte }

func (p signedPolicyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := map[string][]byte{engine.VersionIndexURL: p.body, engine.VersionIndexSignatureURL: p.signature}[r.URL.String()]
	if body == nil {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header), Request: r}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
}

func TestControllerCompositionEnforcesSignedRunnerPolicy(t *testing.T) {
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := releaseauth.TrustedPublicKeys
	releaseauth.TrustedPublicKeys = []string{base64.StdEncoding.EncodeToString(public)}
	t.Cleanup(func() { releaseauth.TrustedPublicKeys = previous })
	now := time.Now().UTC().Truncate(time.Second)
	release := func(version string) engine.VersionInfo {
		return engine.VersionInfo{Version: version, PublishedAt: now.Add(-time.Hour), SupportUntil: now.Add(-time.Hour).AddDate(0, 12, 0)}
	}
	policy := engine.VersionIndex{
		Format: 1, PublishedAt: now, Latest: "v0.30.0",
		Versions: []engine.VersionInfo{release("v0.10.0"), release("v0.25.0"), release("v0.30.0")},
		Minimum:  engine.Restriction{Version: "v0.20.0", Reason: "minimum fixture", Security: true, AnnouncedAt: now, EffectiveAt: now},
		Revoked:  []engine.Restriction{{Version: "v0.25.0", Reason: "revocation fixture", Security: true, AnnouncedAt: now, EffectiveAt: now}},
	}
	policy.Versions = append(policy.Versions, release("v0.20.0"))
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Client{Transport: signedPolicyTransport{body: body, signature: ed25519.Sign(key, body)}}
	for name, claims := range composedClaimants(t, "v0.30.0", transport) {
		for version, want := range map[string]apierr.Code{"v0.10.0": apierr.VersionUnsupported, "v0.25.0": apierr.VersionRevoked, "v0.40.0": apierr.VersionUnsupported, "v0.30.0": ""} {
			err := claimAs(t, claims, version)
			var failure *apierr.Error
			if want == "" && err != nil || want != "" && (!errors.As(err, &failure) || failure.Code != want) {
				t.Fatalf("%s claim as %s: %v, want %q", name, version, err, want)
			}
		}
	}
}
