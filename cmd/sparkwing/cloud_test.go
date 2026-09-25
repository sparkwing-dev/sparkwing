package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const cloudDashboardURL = "https://dash.sparkwing.example"

type cloudFixture struct {
	store      *store.Store
	controller string
	admin      string
	profiles   string
}

// safety: a real controller with auth from its own store, so minting, lookup,
// and revocation run through the handlers an operator's controller runs.
func newCloudFixture(t *testing.T) *cloudFixture {
	t.Helper()
	discovery.ResetCache()
	t.Cleanup(discovery.ResetCache)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().
		WithDashboardURL(cloudDashboardURL).
		Handler())
	t.Cleanup(srv.Close)
	return &cloudFixture{
		store:      st,
		controller: srv.URL,
		admin:      admin,
		profiles:   profilesFixturePath(t),
	}
}

func (f *cloudFixture) userTokens(t *testing.T) []store.Token {
	t.Helper()
	tokens, err := f.store.ListTokens(store.TokenKindUser, true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	return tokens
}

func (f *cloudFixture) mintedToken(t *testing.T, principal string) store.Token {
	t.Helper()
	for _, tok := range f.userTokens(t) {
		if tok.Principal == principal {
			return tok
		}
	}
	t.Fatalf("no user token minted for %q", principal)
	return store.Token{}
}

func (f *cloudFixture) connect(t *testing.T, args ...string) string {
	t.Helper()
	withStdin(t, f.admin+"\n")
	return captureStdout(t, func() {
		full := append([]string{"connect", "--controller", f.controller, "--admin-token-stdin"}, args...)
		if err := runCloud(full); err != nil {
			t.Fatalf("cloud connect: %v", err)
		}
	})
}

func TestCloudConnectMintsAUserTokenAndWritesTheProfile(t *testing.T) {
	f := newCloudFixture(t)
	out := f.connect(t, "--name", "prod")

	minted := f.mintedToken(t, "prod")
	if !slices.Equal(minted.Scopes, cloudUserTokenScopes) {
		t.Errorf("minted scopes = %v, want %v", minted.Scopes, cloudUserTokenScopes)
	}
	if slices.Contains(minted.Scopes, controller.ScopeAdmin) {
		t.Errorf("minted token carries admin: %v", minted.Scopes)
	}

	saved := loadSavedProfile(t, f.profiles, "prod")
	if saved.ControllerURL() != f.controller {
		t.Errorf("stored controller = %q, want %q", saved.ControllerURL(), f.controller)
	}
	if !strings.HasPrefix(saved.ControllerToken(), minted.Prefix) {
		t.Errorf("the profile does not carry the minted token")
	}
	if saved.ControllerToken() == f.admin {
		t.Error("the admin credential reached profiles.yaml")
	}
	for _, want := range []string{"minted user token " + minted.Prefix, "connected profile", cloudDashboardURL, "controller", "auth"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, saved.ControllerToken()) || strings.Contains(out, f.admin) {
		t.Errorf("a raw token reached stdout:\n%s", out)
	}
}

func TestCloudConnectDerivesTheProfileNameFromTheControllerHost(t *testing.T) {
	if got := profileNameForController("https://api.sparkwing.example"); got != "api-sparkwing-example" {
		t.Errorf("name = %q, want api-sparkwing-example", got)
	}
	if got := profileNameForController("http://127.0.0.1:4344"); got != "127-0-0-1" {
		t.Errorf("name = %q, want 127-0-0-1", got)
	}

	f := newCloudFixture(t)
	f.connect(t)
	name := profileNameForController(f.controller)
	if got := loadSavedProfile(t, f.profiles, name).ControllerURL(); got != f.controller {
		t.Errorf("profile %q controller = %q, want %q", name, got, f.controller)
	}
}

func TestCloudConnectRefusesAnExistingProfileAndMintsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	f := newCloudFixture(t)
	f.connect(t, "--name", "prod")
	before := f.mintedToken(t, "prod")

	withStdin(t, f.admin+"\n")
	err := runCloud([]string{"connect", "--controller", f.controller, "--admin-token-stdin", "--name", "prod"})
	if err == nil {
		t.Fatal("expected a second connect under the same name to be refused")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error does not name the escape hatch: %v", err)
	}
	if got := len(f.userTokens(t)); got != 2 {
		t.Errorf("the refusal still minted a token: %d user tokens, want 2", got)
	}
	if got := loadSavedProfile(t, f.profiles, "prod").ControllerToken(); !strings.HasPrefix(got, before.Prefix) {
		t.Error("the refusal replaced the stored token")
	}

	withStdin(t, f.admin+"\n")
	captureStdout(t, func() {
		if err := runCloud([]string{"connect", "--controller", f.controller, "--admin-token-stdin", "--name", "prod", "--force"}); err != nil {
			t.Fatalf("cloud connect --force: %v", err)
		}
	})
	if got := loadSavedProfile(t, f.profiles, "prod").ControllerToken(); strings.HasPrefix(got, before.Prefix) {
		t.Error("--force did not replace the stored token")
	}
}

func TestCloudConnectStoresATokenSuppliedOnStdin(t *testing.T) {
	f := newCloudFixture(t)
	withStdin(t, f.admin+"\n")
	captureStdout(t, func() {
		if err := runCloud([]string{"connect", "--controller", f.controller, "--token-stdin", "--name", "admin-conn"}); err != nil {
			t.Fatalf("cloud connect: %v", err)
		}
	})
	if got := len(f.userTokens(t)); got != 1 {
		t.Errorf("--token-stdin minted a token: %d user tokens, want 1", got)
	}
	if got := loadSavedProfile(t, f.profiles, "admin-conn").ControllerToken(); got != f.admin {
		t.Error("the supplied token did not reach the profile")
	}
}

func TestCloudConnectSetsTheProjectDefault(t *testing.T) {
	f := newCloudFixture(t)
	root := t.TempDir()
	sparkwingDir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"main.go":              "package main\n",
		projectconfig.Filename: "pipelines:\n  - name: build\n    entrypoint: Build\n",
	} {
		if err := os.WriteFile(filepath.Join(sparkwingDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)

	out := f.connect(t, "--name", "prod", "--set-default")
	if !strings.Contains(out, "defaults.profile: prod") {
		t.Errorf("output does not report the project default:\n%s", out)
	}

	path := filepath.Join(sparkwingDir, projectconfig.Filename)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatalf("the written project config does not load: %v", err)
	}
	if cfg.Defaults.Profile != "prod" {
		t.Fatalf("defaults.profile = %q, want prod", cfg.Defaults.Profile)
	}
	if len(cfg.Pipelines) != 1 || cfg.Pipelines[0].Name != "build" {
		t.Errorf("writing the default perturbed the pipelines section: %+v", cfg.Pipelines)
	}

	resolved, chain, _, err := resolveProfileChain("")
	if err != nil {
		t.Fatalf("resolve with no flag: %v", err)
	}
	if resolved == nil || resolved.ControllerURL() != f.controller {
		t.Fatalf("no-flag resolution did not select the connected profile: %+v", resolved)
	}
	if chain.Source != profile.ChainSourceProjectDefault {
		t.Errorf("chain source = %q, want %q", chain.Source, profile.ChainSourceProjectDefault)
	}
}

func TestCloudDisconnectRevokesTheTokenAndRemovesTheProfile(t *testing.T) {
	f := newCloudFixture(t)
	f.connect(t, "--name", "prod")
	minted := f.mintedToken(t, "prod")

	withStdin(t, f.admin+"\n")
	out := captureStdout(t, func() {
		if err := runCloud([]string{"disconnect", "--name", "prod", "--admin-token-stdin"}); err != nil {
			t.Fatalf("cloud disconnect: %v", err)
		}
	})
	if !strings.Contains(out, "revoked "+minted.Prefix) {
		t.Errorf("output does not report the revoke:\n%s", out)
	}
	if after := f.mintedToken(t, "prod"); after.RevokedAt == nil {
		t.Error("the minted token is still live")
	}
	cfg, err := profile.Load(f.profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Profiles["prod"]; ok {
		t.Error("the profile survived the disconnect")
	}
}

func TestCloudDisconnectLeavesATokenItCannotRevoke(t *testing.T) {
	f := newCloudFixture(t)
	f.connect(t, "--name", "prod")

	captureStdout(t, func() {
		if err := runCloud([]string{"disconnect", "--name", "prod"}); err != nil {
			t.Fatalf("cloud disconnect: %v", err)
		}
	})
	if after := f.mintedToken(t, "prod"); after.RevokedAt != nil {
		t.Error("a user-scoped credential revoked its own token")
	}
	cfg, err := profile.Load(f.profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Profiles["prod"]; ok {
		t.Error("the profile survived the disconnect")
	}
}

func TestCloudDisconnectKeepsTheTokenWhenAsked(t *testing.T) {
	f := newCloudFixture(t)
	f.connect(t, "--name", "prod")

	withStdin(t, f.admin+"\n")
	captureStdout(t, func() {
		if err := runCloud([]string{"disconnect", "--name", "prod", "--keep-token", "--admin-token-stdin"}); err != nil {
			t.Fatalf("cloud disconnect: %v", err)
		}
	})
	if after := f.mintedToken(t, "prod"); after.RevokedAt != nil {
		t.Error("--keep-token revoked the token anyway")
	}
}

func TestCloudStatusReportsThePrincipalAndProbes(t *testing.T) {
	f := newCloudFixture(t)
	f.connect(t, "--name", "prod")

	out := captureStdout(t, func() {
		if err := runCloud([]string{"status", "--profile", "prod", "-o", "json"}); err != nil {
			t.Fatalf("cloud status: %v", err)
		}
	})
	var got cloudStatusReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode status: %v\n%s", err, out)
	}
	if got.Profile != "prod" || got.Principal != "prod" {
		t.Errorf("status = %+v, want profile and principal prod", got)
	}
	if got.Controller != f.controller {
		t.Errorf("controller = %q, want %q", got.Controller, f.controller)
	}
	if got.Dashboard != cloudDashboardURL {
		t.Errorf("dashboard = %q, want %q", got.Dashboard, cloudDashboardURL)
	}
	if !got.OK {
		t.Errorf("probes failed: %+v", got.Probes)
	}
	names := make([]string, 0, len(got.Probes))
	for _, p := range got.Probes {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"controller", "auth", "logs"}) {
		t.Errorf("probes = %v, want the configured service probes", names)
	}
}

func TestCloudConnectRefusesAControllerThatIsNotAURL(t *testing.T) {
	newCloudFixture(t)
	err := runCloud([]string{"connect", "--controller", "api.sparkwing.example"})
	if err == nil {
		t.Fatal("expected a scheme-less controller to be refused")
	}
	if !strings.Contains(err.Error(), "http or https") {
		t.Errorf("error = %v, want it to name the accepted schemes", err)
	}
}

func TestCloudConnectRefusesATokenlessConnectionToAnAuthedController(t *testing.T) {
	f := newCloudFixture(t)
	err := runCloud([]string{"connect", "--controller", f.controller, "--name", "prod"})
	if err == nil {
		t.Fatal("expected a tokenless connect to an authenticated controller to be refused")
	}
	if !strings.Contains(err.Error(), "--admin-token-stdin") {
		t.Errorf("error does not name the credential flags: %v", err)
	}
	cfg, loadErr := profile.Load(f.profiles)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := cfg.Profiles["prod"]; ok {
		t.Error("the refusal still wrote a profile")
	}
}

func TestCloudConnectAcceptsAnUnauthenticatedController(t *testing.T) {
	discovery.ResetCache()
	t.Cleanup(discovery.ResetCache)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(controller.New(st, nil).WithDashboardURL(cloudDashboardURL).Handler())
	t.Cleanup(srv.Close)
	profiles := profilesFixturePath(t)

	captureStdout(t, func() {
		if err := runCloud([]string{"connect", "--controller", srv.URL, "--name", "local"}); err != nil {
			t.Fatalf("cloud connect: %v", err)
		}
	})
	if got := loadSavedProfile(t, profiles, "local").ControllerURL(); got != srv.URL {
		t.Errorf("stored controller = %q, want %q", got, srv.URL)
	}
}
