package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/runnersvc"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runnersFixture struct {
	store  *store.Store
	config string
	calls  *[]string
}

// safety: a real controller with auth from its own store, so token minting and
// revocation run through the handlers an operator's controller runs.
func newRunnersFixture(t *testing.T) *runnersFixture {
	t.Helper()
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
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	writeProfilesFixture(t, fmt.Sprintf(
		"profiles:\n  prod:\n    controller: { url: %s, token: %s }\n    logs: { type: controller, url: %s }\n",
		srv.URL, admin, srv.URL))

	calls := []string{}
	home := t.TempDir()
	t.Cleanup(swapRunnerServiceHost(func(configPath string) (runnersvc.Host, error) {
		return runnersvc.Host{
			GOOS:       "linux",
			Home:       home,
			ConfigHome: filepath.Join(home, ".config"),
			Binary:     "/usr/local/bin/sparkwing-runner",
			ConfigPath: configPath,
			Exec: func(name string, args ...string) (string, error) {
				calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
				return "", nil
			},
		}, nil
	}))
	return &runnersFixture{store: st, config: filepath.Join(t.TempDir(), "agent.yaml"), calls: &calls}
}

func swapRunnerServiceHost(fn func(string) (runnersvc.Host, error)) func() {
	prev := runnerServiceHost
	runnerServiceHost = fn
	return func() { runnerServiceHost = prev }
}

func (f *runnersFixture) runnerTokens(t *testing.T) []store.Token {
	t.Helper()
	tokens, err := f.store.ListTokens(store.TokenKindRunner, true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	return tokens
}

func TestRunnersAddMintsAScopedTokenAndWritesTheClaimModeConfig(t *testing.T) {
	f := newRunnersFixture(t)
	out := captureStdout(t, func() {
		if err := runRunners([]string{
			"add", "--profile", "prod", "--name", "dev-laptop",
			"--max-concurrent", "3", "--contribution", "4,8gb", "--labels", "linux,arch=amd64",
			"--config", f.config,
		}); err != nil {
			t.Fatalf("runners add: %v", err)
		}
	})

	tokens := f.runnerTokens(t)
	if len(tokens) != 1 {
		t.Fatalf("minted %d runner tokens, want 1", len(tokens))
	}
	minted := tokens[0]
	if minted.Principal != "agent:dev-laptop" {
		t.Errorf("principal = %q", minted.Principal)
	}
	for _, scope := range runnerTokenScopes {
		if !slices.Contains(minted.Scopes, scope) {
			t.Errorf("minted token is missing scope %q: %v", scope, minted.Scopes)
		}
	}
	if slices.Contains(minted.Scopes, controller.ScopeAdmin) {
		t.Errorf("minted token carries admin: %v", minted.Scopes)
	}

	info, err := os.Stat(f.config)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %04o, want 0600", info.Mode().Perm())
	}
	raw, err := cluster.LoadAgentConfig(f.config)
	if err != nil {
		t.Fatalf("the written config does not load: %v", err)
	}
	if raw.Name != "" || len(raw.Coordinators) != 0 {
		t.Errorf("config selects enrolled mode: name=%q coordinators=%d", raw.Name, len(raw.Coordinators))
	}
	if raw.HolderPrefix != "dev-laptop" || raw.MaxConcurrent != 3 || raw.Contribution != "4,8gb" {
		t.Errorf("config = %+v", raw)
	}
	if !slices.Equal(raw.Labels, []string{"linux", "arch=amd64"}) {
		t.Errorf("labels = %v", raw.Labels)
	}
	if raw.LocalAdmission == nil || !*raw.LocalAdmission {
		t.Error("config did not enable local admission")
	}
	if !strings.HasPrefix(raw.Token, minted.Prefix) {
		t.Error("the config does not carry the minted token")
	}
	cfg, err := cluster.ValidateAgentConfig(*raw)
	if err != nil {
		t.Fatalf("the written config does not validate: %v", err)
	}
	if err := cluster.CheckEnrolledExecutionAvailable(cfg, false); err != nil {
		t.Fatalf("the written config would be refused at startup: %v", err)
	}

	wantCalls := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now sparkwing-runner.service",
	}
	if !slices.Equal(*f.calls, wantCalls) {
		t.Errorf("service calls = %v, want %v", *f.calls, wantCalls)
	}
	for _, want := range []string{minted.Prefix, "wrote " + f.config, "runners remove --profile prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, raw.Token) {
		t.Error("the raw token reached stdout")
	}
}

func TestRunnersAddSkipsTheServiceWhenAsked(t *testing.T) {
	f := newRunnersFixture(t)
	out := captureStdout(t, func() {
		if err := runRunners([]string{
			"add", "--profile", "prod", "--name", "desk",
			"--config", f.config, "--no-service",
		}); err != nil {
			t.Fatalf("runners add: %v", err)
		}
	})
	if len(*f.calls) != 0 {
		t.Errorf("--no-service still drove the service manager: %v", *f.calls)
	}
	if !strings.Contains(out, "agent --config "+f.config) {
		t.Errorf("output does not name the manual start command:\n%s", out)
	}
}

func TestRunnersAddRefusesAnExistingConfigAndMintsNothing(t *testing.T) {
	f := newRunnersFixture(t)
	if err := os.WriteFile(f.config, []byte("controller: http://elsewhere\ntoken: keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runRunners([]string{"add", "--profile", "prod", "--name", "desk", "--config", f.config})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("runners add over an existing config = %v, want a refusal naming --force", err)
	}
	if tokens := f.runnerTokens(t); len(tokens) != 0 {
		t.Errorf("a refused enrollment still minted %d token(s)", len(tokens))
	}
	body, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "keep-me") {
		t.Errorf("the existing config was rewritten: %s", body)
	}
}

func TestRunnersAddForceReplacesTheConfig(t *testing.T) {
	f := newRunnersFixture(t)
	if err := os.WriteFile(f.config, []byte("controller: http://elsewhere\ntoken: replace-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := runRunners([]string{
			"add", "--profile", "prod", "--name", "desk",
			"--config", f.config, "--force", "--no-service",
		}); err != nil {
			t.Fatalf("runners add --force: %v", err)
		}
	})
	body, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "replace-me") {
		t.Errorf("--force did not replace the config: %s", body)
	}
}

func TestRunnersRemoveStopsTheServiceThenRevokesTheToken(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := runRunners([]string{"add", "--profile", "prod", "--name", "desk", "--config", f.config}); err != nil {
			t.Fatalf("runners add: %v", err)
		}
	})
	*f.calls = nil

	out := captureStdout(t, func() {
		if err := runRunners([]string{"remove", "--profile", "prod", "--config", f.config}); err != nil {
			t.Fatalf("runners remove: %v", err)
		}
	})

	tokens := f.runnerTokens(t)
	if len(tokens) != 1 {
		t.Fatalf("listed %d runner tokens, want 1", len(tokens))
	}
	if tokens[0].RevokedAt == nil {
		t.Error("runners remove left the token live")
	}
	if !slices.Contains(*f.calls, "systemctl --user disable --now sparkwing-runner.service") {
		t.Errorf("service calls = %v, want the unit disabled", *f.calls)
	}
	if !strings.Contains(out, "revoked "+tokens[0].Prefix) {
		t.Errorf("output does not name the revoked prefix:\n%s", out)
	}
}

func TestRunnersRemoveWithoutAConfigSaysSo(t *testing.T) {
	f := newRunnersFixture(t)
	err := runRunners([]string{"remove", "--profile", "prod", "--config", f.config})
	if err == nil || !strings.Contains(err.Error(), f.config) {
		t.Fatalf("runners remove with no config = %v, want an error naming the path", err)
	}
}

func TestTokenPrefixMatchesTheStoresPrefixLength(t *testing.T) {
	prefix, err := tokenPrefix("swr_0123456789abcdef")
	if err != nil {
		t.Fatalf("tokenPrefix: %v", err)
	}
	if len(prefix) != store.PrefixLen {
		t.Errorf("prefix = %q, want %d characters", prefix, store.PrefixLen)
	}
	if _, err := tokenPrefix("swr_"); err == nil {
		t.Error("tokenPrefix accepted a token shorter than one prefix")
	}
}
