package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerEnrollmentWritesFinalSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	cfg := agentFileConfig{Controller: "https://controller.example", Token: "private-runner-token", MaxConcurrent: 2, AllowRepos: []string{"github.com/acme/*"}}
	if err := validateAgentFileConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := finishRunnerEnrollment(path, cfg, false, runnerServicePlan{skipped: true}, func() error { t.Error("revoked successful enrollment"); return nil }); err != nil {
		t.Fatal(err)
	}
	final, err := userconfig.ReadConfig(path)
	if err != nil || final.Runner == nil || final.Runner.CredentialsDir == nil || len(final.Runner.Repos) != 1 {
		t.Fatalf("invalid runner configuration: %+v %v", final.Runner, err)
	}
	runner, err := agentconfig.Load(path)
	if err != nil || runner.Token != cfg.Token {
		t.Fatalf("runner could not load minted credential: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(body), cfg.Token) {
		t.Fatalf("credential exposed in YAML: %v", err)
	}
	previous := cfg.Token
	cfg.Token = "replacement-runner-token"
	if _, err := writeAgentConfig(path, cfg, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("existing enrollment was not refused: %v", err)
	}
	runner, err = agentconfig.Load(path)
	if err != nil || runner.Token != previous {
		t.Fatalf("refused replacement changed the credential: %v", err)
	}
	if _, err := writeAgentConfig(path, cfg, true); err != nil {
		t.Fatal(err)
	}
	runner, err = agentconfig.Load(path)
	if err != nil || runner.Token != cfg.Token {
		t.Fatalf("forced replacement did not load the new credential: %v", err)
	}
}

func TestRunnerEnrollmentReachesServiceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	called := false
	service := runnerServicePlan{host: runnerservice.Host{
		GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir(), Binary: "/test/sparkwing-runner", ConfigPath: path,
		Exec: func(_ context.Context, name string, args ...string) (string, error) {
			called = true
			return "fixture service failure", errors.New("service failed")
		},
	}}
	revoked := false
	err := finishRunnerEnrollment(path, agentFileConfig{Controller: "https://controller.example", Token: "private-runner-token", MaxConcurrent: 2}, false, service, func() error { revoked = true; return nil })
	if !called || err == nil || !strings.Contains(err.Error(), "fixture service failure") {
		t.Fatalf("failure did not reach service installation: %v", err)
	}
	final, err := userconfig.ReadConfig(path)
	if err != nil || final.Runner != nil {
		t.Fatalf("service failure left invalid configuration: %v", err)
	}
	if !revoked {
		t.Fatal("service failure left the new authority live")
	}
}

func TestRunnerConfigRollbackPreservesAReplacementEnrollment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	cfg := agentFileConfig{Controller: "https://controller.example", Token: "failed-credential", MaxConcurrent: 1}
	rollback, err := writeAgentConfig(path, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := userconfig.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Token = "replacement-credential"
	if _, err := writeAgentConfig(path, cfg, true); err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	loaded, err := agentconfig.Load(path)
	if err != nil || loaded.Token != cfg.Token {
		t.Fatalf("rollback changed the replacement enrollment: %v", err)
	}
	if _, err := os.Stat(*failed.Runner.CredentialsDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback retained the failed credential: %v", err)
	}
}

func TestRunnerConfigRollbackRestoresAliasedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	before := []byte("run:\n  submit_env_allow: &shared [PATH]\nrunner:\n  controller: https://controller.example\n  labels: *shared\n  repos: []\n")
	if err := fssecure.WriteFile(path, before); err != nil {
		t.Fatal(err)
	}
	rollback, err := writeAgentConfig(path, agentFileConfig{Controller: "https://controller.example", Token: "new-credential", MaxConcurrent: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatalf("could not restore aliased settings: %v", err)
	}
	current, err := userconfig.ReadConfig(path)
	if err != nil || current.Runner == nil || len(current.Runner.Labels) != 1 || current.Runner.Labels[0] != "PATH" || current.Run == nil || len(current.Run.SubmitEnvAllow) != 1 || current.Run.SubmitEnvAllow[0] != "PATH" {
		t.Fatalf("rollback lost aliased settings: %+v %v", current, err)
	}
	if current.Runner.CredentialsDir != nil || current.Runner.MaxConcurrent != nil {
		t.Fatal("rollback added absent settings")
	}
	if current.Runner.Repos == nil || len(current.Runner.Repos) != 0 {
		t.Fatal("rollback lost the explicit empty repository fence")
	}
}

func TestRunnerEnrollmentRemovesNewUnitWhenManagerDisappears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	lost := false
	failure := errors.New("service manager unavailable")
	plan := runnerServicePlan{host: runnerservice.Host{
		GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir(), Binary: "/test/sparkwing-runner", ConfigPath: path,
		Exec: func(_ context.Context, _ string, args ...string) (string, error) {
			lost = lost || strings.Join(args, " ") == "--user daemon-reload"
			if lost {
				return "manager unavailable", failure
			}
			return "", nil
		},
	}}
	revoked := false
	err := finishRunnerEnrollment(path, agentFileConfig{Controller: "https://controller.example", Token: "new-credential", MaxConcurrent: 1}, false, plan, func() error { revoked = true; return nil })
	if !errors.Is(err, failure) || !revoked {
		t.Fatalf("cleanup hid service failure or retained authority: %v", err)
	}
	if _, err := os.Stat(runnerservice.UnitPath(plan.host)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreachable manager left the new registration file: %v", err)
	}
	current, err := userconfig.ReadConfig(path)
	if err != nil || current.Runner != nil {
		t.Fatalf("unreachable manager left its new runner section: %v", err)
	}
}

func TestRunnerEnrollmentFailureKeepsAnExistingService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	cfg := agentFileConfig{Controller: "https://controller.example", Token: "previous-credential", MaxConcurrent: 1}
	if _, err := writeAgentConfig(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	failed := false
	failure := errors.New("manager unavailable")
	plan := runnerServicePlan{host: runnerservice.Host{
		GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir(), Binary: "/test/sparkwing-runner", ConfigPath: path,
		Exec: func(_ context.Context, _ string, args ...string) (string, error) {
			if failed && strings.Join(args, " ") == "--user daemon-reload" {
				return "manager unavailable", failure
			}
			return "", nil
		},
	}}
	if _, err := runnerservice.Install(t.Context(), plan.host); err != nil {
		t.Fatal(err)
	}
	unit := runnerservice.UnitPath(plan.host)
	before, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	failed = true
	cfg.Token = "new-credential"
	err = finishRunnerEnrollment(path, cfg, true, plan, func() error { return nil })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(unit)
	if err != nil || string(after) != string(before) {
		t.Fatalf("cleanup changed the existing service: %v", err)
	}
	previous, err := agentconfig.Load(path)
	if err != nil || previous.Token != "previous-credential" {
		t.Fatalf("cleanup changed the existing credential: %v", err)
	}
}

func TestFailedEnrollmentCleanupKeepsAReplacedServiceFile(t *testing.T) {
	host := runnerservice.Host{
		GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir(), Binary: "/test/sparkwing-runner", ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
		Exec: func(context.Context, string, ...string) (string, error) { return "", nil },
	}
	if _, err := runnerservice.Install(t.Context(), host); err != nil {
		t.Fatal(err)
	}
	unit := runnerservice.UnitPath(host)
	replacement := unit + ".replacement"
	const body = "another writer's service"
	if err := fssecure.WriteFile(replacement, []byte(body)); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("manager unavailable")
	host.Exec = func(context.Context, string, ...string) (string, error) {
		if err := os.Rename(replacement, unit); err != nil {
			t.Fatal(err)
		}
		return "manager unavailable", failure
	}
	if err := cleanupNewRunnerService(host, unit); !errors.Is(err, failure) || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed service was not reported: %v", err)
	}
	after, err := os.ReadFile(unit)
	if err != nil || string(after) != body {
		t.Fatalf("cleanup removed a replacement file: %v", err)
	}
}

func TestRunnerEnrollmentCredentialSurvivesAnotherWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("SPARKWING_CONFIG", "config.yaml")
	path, err := agentConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	const token = "private-enrollment-credential"
	if _, err := writeAgentConfig(path, agentFileConfig{Controller: "https://controller.example", Token: token, MaxConcurrent: 1}, false); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	loaded, err := agentconfig.Load(absolute)
	if err != nil || loaded.Token != token {
		t.Fatalf("enrolled credential depended on the caller's directory: %v", err)
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(loaded.CredentialsDir) {
		t.Fatal("enrollment did not capture absolute configuration and credential paths")
	}
}

func TestRunnerEnrollmentFailurePreservesPriorSettingsAndCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	previous := agentFileConfig{Controller: "https://controller.example", Token: "previous-runner-token", Labels: []string{"old"}, MaxConcurrent: 1}
	if _, err := writeAgentConfig(path, previous, false); err != nil {
		t.Fatal(err)
	}
	prior, err := userconfig.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var created string
	failure, revokeFailure := errors.New("service failed"), errors.New("revoke failed")
	plan := runnerServicePlan{host: runnerservice.Host{
		GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir(), Binary: "/test/sparkwing-runner", ConfigPath: path,
		Exec: func(_ context.Context, _ string, args ...string) (string, error) {
			if strings.Join(args, " ") != "--user daemon-reload" || created != "" {
				return "", nil
			}
			current, err := userconfig.ReadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			created = *current.Runner.CredentialsDir
			if err := userconfig.Write(path, userconfig.Debug, "test settings", userconfig.DebugConfig{PauseTimeout: new("7m")}); err != nil {
				t.Fatal(err)
			}
			return "fixture failure", failure
		},
	}}
	revocations := 0
	err = finishRunnerEnrollment(path, agentFileConfig{Controller: previous.Controller, Token: "new-runner-token", Labels: []string{"new"}, MaxConcurrent: 2}, true, plan, func() error {
		revocations++
		return revokeFailure
	})
	if !errors.Is(err, failure) || !errors.Is(err, revokeFailure) || revocations != 1 {
		t.Fatalf("failure lost enrollment or revocation evidence: %v, calls=%d", err, revocations)
	}
	current, err := userconfig.ReadConfig(path)
	if err != nil || current.Runner == nil || *current.Runner.CredentialsDir != *prior.Runner.CredentialsDir || current.Debug == nil || *current.Debug.PauseTimeout != "7m" {
		t.Fatalf("rollback replaced prior or concurrent settings: %+v %v", current, err)
	}
	loaded, err := agentconfig.Load(path)
	if err != nil || loaded.Token != previous.Token || len(loaded.Labels) != 1 || loaded.Labels[0] != "old" {
		t.Fatalf("rollback did not restore prior runner: %+v %v", loaded, err)
	}
	if created == "" {
		t.Fatal("service did not reach the new configuration")
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed enrollment left its private files: %v", err)
	}
	if _, err := os.Stat(runnerservice.UnitPath(plan.host)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed enrollment left its new service: %v", err)
	}
}

func TestRunnerEnrollmentWriteFailureCleansOnlyNewCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	body := []byte("runner:\n  controller: https://controller.example\n  labels: &shared [PATH]\nrun:\n  submit_env_allow: *shared\n")
	if err := fssecure.WriteFile(path, body); err != nil {
		t.Fatal(err)
	}
	revoked := false
	err := finishRunnerEnrollment(path, agentFileConfig{Controller: "https://controller.example", Token: "new-runner-token", MaxConcurrent: 1}, true, runnerServicePlan{skipped: true}, func() error { revoked = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "anchor") || !revoked {
		t.Fatalf("failed config publication did not revoke: %v, revoked=%v", err, revoked)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(body) {
		t.Fatalf("failed publication changed existing settings: %v", err)
	}
	residue, err := filepath.Glob(filepath.Join(filepath.Dir(path), "runner-credentials-*"))
	if err != nil || len(residue) != 0 {
		t.Fatalf("failed publication left private files: %v %v", residue, err)
	}
}

func TestRunnerEnrollmentRejectsBadRepoPattern(t *testing.T) {
	err := validateAgentFileConfig(agentFileConfig{Controller: "https://controller.example", MaxConcurrent: 2, AllowRepos: []string{"https://github.com/acme/*"}})
	if err == nil || !strings.Contains(err.Error(), "runner.repos") || !strings.Contains(err.Error(), "invalid repository allow-list") {
		t.Fatalf("expected repository pattern refusal: %v", err)
	}
}
