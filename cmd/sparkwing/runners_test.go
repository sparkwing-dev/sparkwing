package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/agentservice"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runnersFixture struct {
	config    string
	calls     *[]string
	tokens    []controllerwire.TokenMetadata
	created   []controllerwire.TokenCreate
	timeline  []string
	mintErr   error
	revokeErr error
	failExec  map[string]string
	binary    string
}

func newRunnersFixture(t *testing.T) *runnersFixture {
	t.Helper()
	writeProfilesFixture(t, "profiles:\n  prod:\n    controller: { url: https://controller.example, token: swk_operator-secret }\n")
	calls := []string{}
	home := t.TempDir()
	f := &runnersFixture{config: filepath.Join(t.TempDir(), "config.yaml"), calls: &calls, failExec: map[string]string{}, binary: "/usr/local/bin/sparkwing-runner"}
	if runtime.GOOS == "windows" {
		f.binary = "C:/usr/local/bin/sparkwing-runner.exe"
	}
	t.Cleanup(swapRunnerServiceHost(func(configPath string) (agentservice.Host, error) {
		return agentservice.Host{
			GOOS: "linux", Home: home, ConfigHome: filepath.Join(home, ".config"), Binary: f.binary, ConfigPath: configPath,
			Exec: func(name string, args ...string) (string, error) {
				call := strings.TrimSpace(name + " " + strings.Join(args, " "))
				calls = append(calls, call)
				f.timeline = append(f.timeline, call)
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

func (f *runnersFixture) run(args []string) error {
	ctx := context.Background()
	args = append(args, "--output", "pretty")
	switch args[0] {
	case "add":
		enrollment, _, err := prepareRunnerEnrollment(args[1:])
		if err != nil {
			return err
		}
		return enrollRunner(ctx, f, enrollment)
	case "remove":
		removal, _, err := prepareRunnerRemoval(args[1:])
		if err != nil {
			return err
		}
		return removeRunner(ctx, f, removal)
	}
	return errors.New("unexpected command")
}

func (f *runnersFixture) ListTokens(context.Context, controllerwire.TokenQuery) (controllerwire.Tokens, error) {
	return controllerwire.Tokens{Tokens: f.tokens}, nil
}

func (f *runnersFixture) ReadToken(_ context.Context, prefix string) (controllerwire.TokenMetadata, error) {
	for _, token := range f.tokens {
		if token.Prefix == prefix {
			return token, nil
		}
	}
	return controllerwire.TokenMetadata{}, errors.New("token missing")
}

func (f *runnersFixture) CreateToken(_ context.Context, input controllerwire.TokenCreate) (controllerwire.TokenCreated, error) {
	f.created = append(f.created, input)
	if f.mintErr != nil {
		return controllerwire.TokenCreated{}, f.mintErr
	}
	prefix := fmt.Sprintf("swk_%08d", len(f.tokens)+1)
	token := controllerwire.TokenMetadata{Prefix: prefix, Kind: input.Kind, Principal: input.Principal, Scopes: input.Scopes}
	f.tokens = append(f.tokens, token)
	return controllerwire.TokenCreated{Token: prefix + "_secret", Metadata: token}, nil
}

func (f *runnersFixture) RotateToken(context.Context, string, controllerwire.TokenRotate) (controllerwire.TokenRotated, error) {
	panic("unexpected rotate")
}

func (f *runnersFixture) RevokeToken(_ context.Context, prefix string) (controllerwire.TokenRevoked, error) {
	f.timeline = append(f.timeline, "revoke")
	if f.revokeErr != nil {
		return controllerwire.TokenRevoked{}, f.revokeErr
	}
	for i, token := range f.tokens {
		if token.Prefix == prefix {
			revoked := int64(42)
			f.tokens[i].RevokedAt = &revoked
			return controllerwire.TokenRevoked{Prefix: prefix, Revoked: true}, nil
		}
	}
	return controllerwire.TokenRevoked{}, errors.New("token missing")
}

func swapRunnerServiceHost(fn func(string) (agentservice.Host, error)) func() {
	prev := runnerServiceHost
	previousGOOS := runnerServiceGOOS
	runnerServiceGOOS = "linux"
	runnerServiceHost = fn
	return func() { runnerServiceHost = prev; runnerServiceGOOS = previousGOOS }
}

func (f *runnersFixture) runnerTokens(t *testing.T) []controllerwire.TokenMetadata {
	t.Helper()
	var result []controllerwire.TokenMetadata
	for _, token := range f.tokens {
		if token.Kind == "runner" {
			result = append(result, token)
		}
	}
	return result
}

func TestRunnersAddMintsAScopedTokenAndWritesTheClaimModeConfig(t *testing.T) {
	f := newRunnersFixture(t)
	out := captureStdout(t, func() {
		if err := f.run([]string{
			"add", "--profile", "prod", "--runner", "dev-laptop",
			"--max-concurrent", "3", "--contribution", "4,8gb", "--label", "linux,arch=amd64",
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
	if minted.Principal != "runner:dev-laptop" {
		t.Errorf("principal = %q", minted.Principal)
	}
	for _, scope := range []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.RunsState} {
		if !slices.Contains(minted.Scopes, scope) {
			t.Errorf("minted token is missing scope %q: %v", scope, minted.Scopes)
		}
	}
	if slices.Contains(minted.Scopes, capability.TeamAdmin) {
		t.Errorf("minted token carries admin: %v", minted.Scopes)
	}

	info, err := os.Stat(f.config)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if runtime.GOOS == "windows" {
		if err := fssecure.VerifyPrivateConfig(f.config, info); err != nil {
			t.Fatal(err)
		}
	} else if info.Mode().Perm() != 0o600 {
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
	for _, want := range []string{
		minted.Prefix, "wrote the agent section of " + f.config, "runners remove --profile prod",
		"docs read --topic threat-model",
	} {
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
		if err := f.run([]string{
			"add", "--profile", "prod", "--runner", "desk",
			"--config", f.config, "--no-service",
		}); err != nil {
			t.Fatalf("runners add: %v", err)
		}
	})
	if len(*f.calls) != 0 {
		t.Errorf("--no-service still drove the service manager: %v", *f.calls)
	}
	if !strings.Contains(out, `"agent" "--config" "`+f.config) {
		t.Errorf("output does not name the manual start command:\n%s", out)
	}
}

func TestRunnersAddRefusesAnExistingConfigAndMintsNothing(t *testing.T) {
	f := newRunnersFixture(t)
	if err := fssecure.WriteFile(f.config, []byte("agent:\n  controller: http://elsewhere\n  token: keep-me\n")); err != nil {
		t.Fatal(err)
	}
	err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config})
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
	if err := fssecure.WriteFile(f.config, []byte("# desk\nrepos:\n  fallback_paths: [~/desk]\nagent:\n  controller: http://elsewhere\n  token: replace-me\n")); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := f.run([]string{
			"add", "--profile", "prod", "--runner", "desk",
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
		t.Errorf("--force did not replace the agent section: %s", body)
	}
	if !strings.Contains(string(body), "# desk") || !strings.Contains(string(body), "fallback_paths: [~/desk]") {
		t.Errorf("--force lost the file's other sections: %s", body)
	}
}

func TestRunnersRemoveStopsTheServiceThenRevokesTheToken(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config}); err != nil {
			t.Fatalf("runners add: %v", err)
		}
	})
	*f.calls = nil

	out := captureStdout(t, func() {
		if err := f.run([]string{"remove", "--profile", "prod", "--config", f.config}); err != nil {
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
	err := f.run([]string{"remove", "--profile", "prod", "--config", f.config})
	if err == nil || !strings.Contains(err.Error(), f.config) {
		t.Fatalf("runners remove with no config = %v, want an error naming the path", err)
	}
}

func TestTokenPrefixMatchesTheStoresPrefixLength(t *testing.T) {
	prefix, err := tokenPrefix("swk_0123456789abcdef")
	if err != nil {
		t.Fatalf("tokenPrefix: %v", err)
	}
	if len(prefix) != store.PrefixLen {
		t.Errorf("prefix = %q, want %d characters", prefix, store.PrefixLen)
	}
	if _, err := tokenPrefix("swk_"); err == nil {
		t.Error("tokenPrefix accepted a token shorter than one prefix")
	}
}

func TestRunnersAddNamesTheRevokeCommandWhenTheServiceInstallFails(t *testing.T) {
	f := newRunnersFixture(t)
	f.failExec["systemctl --user daemon-reload"] = "Failed to connect to bus: No medium found"

	var err error
	out := captureStderr(t, func() {
		err = f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config})
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
		"the token " + prefix + " is live",
		"sparkwing tokens revoke --profile prod --token " + prefix,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunnersAddRefusesAMachineWithoutTheRunnerBinary(t *testing.T) {
	f := newRunnersFixture(t)
	f.binary = ""

	err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config})
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

	err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config})
	if err == nil || !strings.Contains(err.Error(), "systemd user session is unreachable") {
		t.Fatalf("runners add on a host with no user session = %v", err)
	}
	if tokens := f.runnerTokens(t); len(tokens) != 0 {
		t.Errorf("a host that cannot supervise a runner still minted %d token(s)", len(tokens))
	}
}

func TestRunnersAddRefusesAnInvalidContributionBeforeMinting(t *testing.T) {
	f := newRunnersFixture(t)
	err := f.run([]string{
		"add", "--profile", "prod", "--runner", "desk",
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
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "first", "--config", f.config}); err != nil {
			t.Fatalf("first add: %v", err)
		}
	})
	first, err := agentconfig.Load(f.config)
	if err != nil {
		t.Fatalf("load after the first add: %v", err)
	}
	captureStdout(t, func() {
		if err := f.run([]string{
			"add", "--profile", "prod", "--runner", "second",
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
	if runtime.GOOS == "windows" {
		if err := fssecure.VerifyPrivateConfig(f.config, info); err != nil {
			t.Fatal(err)
		}
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode after a replace = %04o, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(f.config))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
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
	err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config, "--force"})
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
	raw := "swk_service1_secret"
	f.tokens = append(f.tokens, controllerwire.TokenMetadata{Prefix: raw[:store.PrefixLen], Kind: "member", Principal: "agent:deploy-bot", Scopes: []capability.Grant{capability.RunsRead}})
	body := fmt.Sprintf("agent:\n  controller: https://controller.example\n  token: %s\n  holder_prefix: desk\n", raw)
	if err := fssecure.WriteFile(f.config, []byte(body)); err != nil {
		t.Fatal(err)
	}

	err := f.run([]string{"remove", "--profile", "prod", "--config", f.config})
	if err == nil {
		t.Fatal("runners remove revoked a member token inferred from principal text")
	}
	for _, want := range []string{"member", "deploy-bot", "not a runner token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	tokens := f.tokens
	if len(tokens) != 1 || tokens[0].RevokedAt != nil {
		t.Error("the member token was revoked anyway")
	}
	if len(*f.calls) != 0 {
		t.Errorf("a refused remove still drove the service manager: %v", *f.calls)
	}
}

func TestRunnersAddWritesTheAllowList(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{
			"add", "--profile", "prod", "--runner", "dev-laptop", "--config", f.config,
			"--repos", "github.com/acme/*", "--repos", "gitlab.example.com/acme/tools",
		}); err != nil {
			t.Fatalf("runners add: %v", err)
		}
	})
	raw, err := agentconfig.Load(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(raw.AllowRepos, []string{"github.com/acme/*", "gitlab.example.com/acme/tools"}) {
		t.Fatalf("allow_repos = %v", raw.AllowRepos)
	}
	cfg, err := agentconfig.Validate(*raw)
	if err != nil || cfg.Gitcache != "" {
		t.Fatalf("the written config = gitcache %q, %v; want direct source", cfg.Gitcache, err)
	}
	if len(f.runnerTokens(t)) != 1 {
		t.Fatal("runners add minted no token")
	}
}

func TestRunnersAddRefusesABadAllowPatternBeforeMinting(t *testing.T) {
	f := newRunnersFixture(t)
	err := f.run([]string{
		"add", "--profile", "prod", "--runner", "dev-laptop", "--config", f.config,
		"--repos", "https://github.com/acme/*",
	})
	if err == nil || !strings.Contains(err.Error(), "allow_repos") {
		t.Fatalf("runners add = %v, want the pattern refused", err)
	}
	if n := len(f.runnerTokens(t)); n != 0 {
		t.Fatalf("minted %d tokens for a config that cannot run", n)
	}
}

func TestRunnersEnrollmentSendsOneKeyAndPreservesRepeatedSelectors(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service", "--label", "os=linux,arch=amd64", "--label", "gpu", "--repos", "github.com/acme/*,gitlab.example.com/acme/*"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(f.created) != 1 || f.created[0].IdempotencyKey == "" {
		t.Fatalf("issuance: %+v", f.created)
	}
	raw, err := agentconfig.Load(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(raw.Labels, []string{"os=linux", "arch=amd64", "gpu"}) || !slices.Equal(raw.AllowRepos, []string{"github.com/acme/*", "gitlab.example.com/acme/*"}) {
		t.Fatalf("selectors: %+v", raw)
	}
}

func TestRunnersEnrollmentJSONDoesNotContainBearerOrPrettyProse(t *testing.T) {
	f := newRunnersFixture(t)
	enrollment, _, err := prepareRunnerEnrollment([]string{"--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service", "--output", "json"})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := enrollRunner(t.Context(), f, enrollment); err != nil {
			t.Fatal(err)
		}
	})
	var result runnerEnrollmentResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Runner != "desk" || result.Token.Kind != "runner" || result.Token.Prefix == "" || result.Service.Status != "skipped" {
		t.Fatalf("enrollment: %+v", result)
	}
	if strings.Contains(out, result.Token.Prefix+"_secret") || strings.Contains(out, "minted runner") {
		t.Fatalf("mixed or secret output: %s", out)
	}
}

func TestRunnersRemovalJSONConfirmsRevocation(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service"}); err != nil {
			t.Fatal(err)
		}
	})
	removal, _, err := prepareRunnerRemoval([]string{"--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service", "--output", "json"})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := removeRunner(t.Context(), f, removal); err != nil {
			t.Fatal(err)
		}
	})
	var result struct {
		Runner string                      `json:"runner"`
		Token  controllerwire.TokenRevoked `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Runner != "desk" || !result.Token.Revoked || result.Token.Prefix != f.tokens[0].Prefix {
		t.Fatalf("revocation: %+v", result)
	}
}

func TestRunnersRemoveRejectsAnotherNameOrControllerBeforeStopping(t *testing.T) {
	for _, scenario := range []string{"name", "controller"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRunnersFixture(t)
			captureStdout(t, func() {
				if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service"}); err != nil {
					t.Fatal(err)
				}
			})
			args := []string{"remove", "--profile", "prod", "--runner", "other", "--config", f.config}
			if scenario == "controller" {
				args = []string{"remove", "--profile", "prod", "--config", f.config}
				body, err := os.ReadFile(f.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := fssecure.WriteFile(f.config, []byte(strings.ReplaceAll(string(body), "https://controller.example", "https://other.example"))); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.run(args); err == nil {
				t.Fatal("mismatched selection accepted")
			}
			if len(*f.calls) != 0 || f.tokens[0].RevokedAt != nil {
				t.Fatalf("mismatched removal mutated: %v %+v", *f.calls, f.tokens)
			}
		})
	}
}

func TestRunnersEnrollmentRequiresProvisioningContractForPools(t *testing.T) {
	_, _, err := prepareRunnerEnrollment([]string{"--runner", "desk", "--reserved-for", "prod"})
	var structured *apierr.Error
	if !errors.As(err, &structured) || structured.Code != apierr.PolicyMissing {
		t.Fatalf("pool proposal: %v", err)
	}
}

func TestRunnersRemovalStopsBeforeRevocation(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config}); err != nil {
			t.Fatal(err)
		}
	})
	f.timeline = nil
	captureStdout(t, func() {
		if err := f.run([]string{"remove", "--profile", "prod", "--config", f.config}); err != nil {
			t.Fatal(err)
		}
	})
	stopped := slices.Index(f.timeline, "systemctl --user disable --now sparkwing-runner.service")
	revoked := slices.Index(f.timeline, "revoke")
	if stopped < 0 || revoked <= stopped {
		t.Fatalf("removal order: %v", f.timeline)
	}
}

func TestRunnersRemovalKeepsCredentialLiveWhenStoppingFails(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config}); err != nil {
			t.Fatal(err)
		}
	})
	f.failExec["systemctl --user disable"] = "service cannot stop"
	f.timeline = nil
	var runErr error
	out := captureStdout(t, func() { runErr = f.run([]string{"remove", "--profile", "prod", "--config", f.config}) })
	if runErr == nil || out != "" || slices.Contains(f.timeline, "revoke") || f.tokens[0].RevokedAt != nil {
		t.Fatalf("failed stop: %q %v %v %+v", out, runErr, f.timeline, f.tokens)
	}
}

func TestRunnersRemovalDoesNotReportSuccessWhenRevocationFails(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service"}); err != nil {
			t.Fatal(err)
		}
	})
	f.revokeErr = &apierr.Error{Code: apierr.Forbidden, Message: "revocation refused"}
	var runErr error
	out := captureStdout(t, func() { runErr = f.run([]string{"remove", "--profile", "prod", "--config", f.config, "--no-service"}) })
	if !errors.Is(runErr, f.revokeErr) || out != "" || f.tokens[0].RevokedAt != nil {
		t.Fatalf("failed revoke: %q %v %+v", out, runErr, f.tokens)
	}
	if _, err := agentconfig.Load(f.config); err != nil {
		t.Fatalf("live credential config lost: %v", err)
	}
}

func TestRunnersEnrollmentRefusesIssuanceWithoutWritingConfig(t *testing.T) {
	f := newRunnersFixture(t)
	f.mintErr = &apierr.Error{Code: apierr.Forbidden, Message: "enrollment refused"}
	var runErr error
	out := captureStdout(t, func() {
		runErr = f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service"})
	})
	if !errors.Is(runErr, f.mintErr) || out != "" || len(f.tokens) != 0 {
		t.Fatalf("refused enrollment: %q %v %+v", out, runErr, f.tokens)
	}
	if _, err := os.Stat(f.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config after refused mint: %v", err)
	}
}
