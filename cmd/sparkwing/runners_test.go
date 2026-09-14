package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/agentservice"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runnersFixture struct {
	store  *store.Store
	config string
	calls  *[]string

	// safety: a command prefix maps to the output its failure prints, so one test breaks one call.
	failExec map[string]string

	// safety: an empty binary stands for a machine without sparkwing-runner installed.
	binary string
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
	f := &runnersFixture{
		store:    st,
		config:   filepath.Join(t.TempDir(), "agent.yaml"),
		calls:    &calls,
		failExec: map[string]string{},
		binary:   "/usr/local/bin/sparkwing-runner",
	}
	t.Cleanup(swapRunnerServiceHost(func(configPath string) (agentservice.Host, error) {
		return agentservice.Host{
			GOOS:       "linux",
			Home:       home,
			ConfigHome: filepath.Join(home, ".config"),
			Binary:     f.binary,
			ConfigPath: configPath,
			Exec: func(name string, args ...string) (string, error) {
				call := strings.TrimSpace(name + " " + strings.Join(args, " "))
				calls = append(calls, call)
				for prefix, out := range f.failExec {
					if strings.HasPrefix(call, prefix) {
						return out, errors.New("exit status 1")
					}
				}
				return "", nil
			},
		}, nil
	}))
	return f
}

func swapRunnerServiceHost(fn func(string) (agentservice.Host, error)) func() {
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
	raw, err := agentconfig.Load(f.config)
	if err != nil {
		t.Fatalf("the written config does not load: %v", err)
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
	if _, err := agentconfig.Validate(*raw); err != nil {
		t.Fatalf("the written config does not validate: %v", err)
	}

	wantCalls := []string{
		"systemctl --user show -p Version --value",
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

func TestRunnersAddNamesTheRevokeCommandWhenTheServiceInstallFails(t *testing.T) {
	f := newRunnersFixture(t)
	f.failExec["systemctl --user daemon-reload"] = "Failed to connect to bus: No medium found"

	var err error
	out := captureStdout(t, func() {
		err = runRunners([]string{"add", "--profile", "prod", "--name", "desk", "--config", f.config})
	})
	if err == nil {
		t.Fatal("a failed service install reported success")
	}

	tokens := f.runnerTokens(t)
	if len(tokens) != 1 {
		t.Fatalf("minted %d tokens, want 1", len(tokens))
	}
	prefix := tokens[0].Prefix
	if tokens[0].RevokedAt != nil {
		t.Error("the failed install revoked the token behind the operator's back")
	}
	for _, want := range []string{
		"minted runner token " + prefix,
		"the token " + prefix + " is live",
		"sparkwing cluster tokens revoke --profile prod --prefix " + prefix,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunnersAddRefusesAMachineWithoutTheRunnerBinary(t *testing.T) {
	f := newRunnersFixture(t)
	f.binary = ""

	err := runRunners([]string{"add", "--profile", "prod", "--name", "desk", "--config", f.config})
	if err == nil || !strings.Contains(err.Error(), "sparkwing-runner is not on PATH") {
		t.Fatalf("runners add without the runner binary = %v, want a refusal naming the binary", err)
	}
	if !strings.Contains(err.Error(), "--no-service") {
		t.Errorf("the refusal does not name the escape hatch: %v", err)
	}
	if tokens := f.runnerTokens(t); len(tokens) != 0 {
		t.Errorf("a machine that cannot run the service still minted %d token(s)", len(tokens))
	}
	if _, err := os.Stat(f.config); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat config = %v, want not-exist", err)
	}
}

func TestRunnersAddRefusesAnUnreachableServiceManagerBeforeMinting(t *testing.T) {
	f := newRunnersFixture(t)
	f.failExec["systemctl --user show"] = "Failed to connect to bus: No medium found"

	err := runRunners([]string{"add", "--profile", "prod", "--name", "desk", "--config", f.config})
	if err == nil || !strings.Contains(err.Error(), "systemd user session is unreachable") {
		t.Fatalf("runners add on a host with no user session = %v", err)
	}
	if tokens := f.runnerTokens(t); len(tokens) != 0 {
		t.Errorf("a host that cannot supervise a runner still minted %d token(s)", len(tokens))
	}
}

func TestRunnersAddRefusesAnInvalidContributionBeforeMinting(t *testing.T) {
	f := newRunnersFixture(t)
	err := runRunners([]string{
		"add", "--profile", "prod", "--name", "desk",
		"--contribution", "not-a-budget", "--config", f.config,
	})
	if err == nil || !strings.Contains(err.Error(), "contribution") {
		t.Fatalf("runners add with a bad contribution = %v, want an error naming contribution", err)
	}
	if tokens := f.runnerTokens(t); len(tokens) != 0 {
		t.Errorf("an invalid config still minted %d token(s)", len(tokens))
	}
	if len(*f.calls) != 0 {
		t.Errorf("an invalid config still drove the service manager: %v", *f.calls)
	}
}

func TestRunnersAddForceTwiceKeepsTheLatestConfigReadable(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := runRunners([]string{"add", "--profile", "prod", "--name", "first", "--config", f.config}); err != nil {
			t.Fatalf("first add: %v", err)
		}
	})
	first, err := agentconfig.Load(f.config)
	if err != nil {
		t.Fatalf("load after the first add: %v", err)
	}
	captureStdout(t, func() {
		if err := runRunners([]string{
			"add", "--profile", "prod", "--name", "second",
			"--config", f.config, "--force",
		}); err != nil {
			t.Fatalf("second add --force: %v", err)
		}
	})
	second, err := agentconfig.Load(f.config)
	if err != nil {
		t.Fatalf("load after the second add: %v", err)
	}
	if second.HolderPrefix != "second" {
		t.Errorf("holder_prefix = %q, want the second name", second.HolderPrefix)
	}
	if second.Token == first.Token {
		t.Error("the second add reused the first token")
	}
	if len(f.runnerTokens(t)) != 2 {
		t.Errorf("minted %d tokens over two adds, want 2", len(f.runnerTokens(t)))
	}
	info, err := os.Stat(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode after a replace = %04o, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(f.config))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agent-") {
			t.Errorf("a temporary config was left behind: %s", e.Name())
		}
	}
}

func TestRunnersAddRefusesASymlinkedConfig(t *testing.T) {
	f := newRunnersFixture(t)
	target := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("controller: http://elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.config); err != nil {
		t.Skipf("this filesystem refuses symlinks: %v", err)
	}
	err := runRunners([]string{"add", "--profile", "prod", "--name", "desk", "--config", f.config, "--force"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("runners add onto a symlink = %v, want a refusal naming the symlink", err)
	}
	if tokens := f.runnerTokens(t); len(tokens) != 0 {
		t.Errorf("a refused symlink write still minted %d token(s)", len(tokens))
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "elsewhere") {
		t.Errorf("the symlink target was rewritten: %s", body)
	}
}

func TestRunnersRemoveRefusesANonRunnerToken(t *testing.T) {
	f := newRunnersFixture(t)
	raw, _, err := f.store.CreateToken("deploy-bot", store.TokenKindService,
		[]string{controller.ScopeRunsRead}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	body := fmt.Sprintf("controller: http://localhost:4344\ntoken: %s\nholder_prefix: desk\n", raw)
	if err := os.WriteFile(f.config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runRunners([]string{"remove", "--profile", "prod", "--config", f.config})
	if err == nil {
		t.Fatal("runners remove revoked a service token")
	}
	for _, want := range []string{"service", "deploy-bot", "not a runner token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	tokens, err := f.store.ListTokens(store.TokenKindService, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].RevokedAt != nil {
		t.Error("the service token was revoked anyway")
	}
	if len(*f.calls) != 0 {
		t.Errorf("a refused remove still drove the service manager: %v", *f.calls)
	}
}
