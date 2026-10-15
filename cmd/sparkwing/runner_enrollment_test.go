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
	cfg := userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), MaxConcurrent: preferencePointer(2), Repos: []string{"github.com/acme/*"}}
	bearer := "private-runner-token"
	if err := validateRunnerEnrollment(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := finishRunnerEnrollment(path, cfg, bearer, false, runnerServicePlan{skipped: true}, func() error { t.Error("revoked successful enrollment"); return nil }); err != nil {
		t.Fatal(err)
	}
	final, err := userconfig.ReadConfig(path)
	if err != nil || final.Runner == nil || final.Runner.CredentialsDir == nil || len(final.Runner.Repos) != 1 {
		t.Fatalf("invalid runner configuration: %+v %v", final.Runner, err)
	}
	runner, err := agentconfig.Load(path)
	if err != nil || runner.Token != bearer {
		t.Fatalf("runner could not load minted credential: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(body), bearer) {
		t.Fatalf("credential exposed in YAML: %v", err)
	}
	previous := bearer
	bearer = "replacement-runner-token"
	if _, err := writeRunnerConfig(path, cfg, bearer, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("existing enrollment was not refused: %v", err)
	}
	runner, err = agentconfig.Load(path)
	if err != nil || runner.Token != previous {
		t.Fatalf("refused replacement changed the credential: %v", err)
	}
	if _, err := writeRunnerConfig(path, cfg, bearer, true); err != nil {
		t.Fatal(err)
	}
	runner, err = agentconfig.Load(path)
	if err != nil || runner.Token != bearer {
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
	_, err := finishRunnerEnrollment(path, userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), MaxConcurrent: preferencePointer(2)}, "private-runner-token", false, service, func() error { return nil })
	if !called || err == nil || !strings.Contains(err.Error(), "fixture service failure") {
		t.Fatalf("failure did not reach service installation: %v", err)
	}
	if cfg, err := userconfig.ReadConfig(path); err != nil || cfg.Runner != nil {
		t.Fatalf("service failure retained runner configuration: %+v %v", cfg, err)
	}
	if dirs, err := filepath.Glob(filepath.Join(filepath.Dir(path), "runner-credentials-*")); err != nil || len(dirs) != 0 {
		t.Fatalf("service failure left credential directories: %v %v", dirs, err)
	}
}

func TestRunnerConfigRollbackPreservesAReplacementEnrollment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	cfg := userconfig.RunnerConfig{Controller: new("https://controller.example"), MaxConcurrent: new(1)}
	bearer := "failed-credential"
	written, err := writeRunnerConfig(path, cfg, bearer, false)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := userconfig.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	bearer = "replacement-credential"
	if _, err := writeRunnerConfig(path, cfg, bearer, true); err != nil {
		t.Fatal(err)
	}
	if err := written.rollback(); err != nil {
		t.Fatal(err)
	}
	loaded, err := agentconfig.Load(path)
	if err != nil || loaded.Token != bearer {
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
	written, err := writeRunnerConfig(path, userconfig.RunnerConfig{Controller: new("https://controller.example"), MaxConcurrent: new(1)}, "new-credential", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := written.rollback(); err != nil {
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
	_, err := finishRunnerEnrollment(path, userconfig.RunnerConfig{Controller: new("https://controller.example"), MaxConcurrent: new(1)}, "new-credential", false, plan, func() error { revoked = true; return nil })
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
	cfg := userconfig.RunnerConfig{Controller: new("https://controller.example"), MaxConcurrent: new(1)}
	bearer := "previous-credential"
	if _, err := writeRunnerConfig(path, cfg, bearer, false); err != nil {
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
	bearer = "new-credential"
	_, err = finishRunnerEnrollment(path, cfg, bearer, true, plan, func() error { return nil })
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
	if _, err := writeRunnerConfig(path, userconfig.RunnerConfig{Controller: new("https://controller.example"), MaxConcurrent: new(1)}, token, false); err != nil {
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
	previous := userconfig.RunnerConfig{Controller: new("https://controller.example"), Labels: []string{"old"}, MaxConcurrent: new(1)}
	const previousToken = "previous-runner-token"
	if _, err := writeRunnerConfig(path, previous, previousToken, false); err != nil {
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
	_, err = finishRunnerEnrollment(path, userconfig.RunnerConfig{Controller: previous.Controller, Labels: []string{"new"}, MaxConcurrent: new(2)}, "new-runner-token", true, plan, func() error {
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
	if err != nil || loaded.Token != previousToken || len(loaded.Labels) != 1 || loaded.Labels[0] != "old" {
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
	_, err := finishRunnerEnrollment(path, userconfig.RunnerConfig{Controller: new("https://controller.example"), MaxConcurrent: new(1)}, "new-runner-token", true, runnerServicePlan{skipped: true}, func() error { revoked = true; return nil })
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
	err := validateRunnerEnrollment(userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), MaxConcurrent: preferencePointer(2), Repos: []string{"https://github.com/acme/*"}})
	if err == nil || !strings.Contains(err.Error(), "runner.repos") || !strings.Contains(err.Error(), "invalid repository allow-list") {
		t.Fatalf("expected repository pattern refusal: %v", err)
	}
}

func TestRunnerEnrollmentFailurePreservesPreviousForcedConfigAndCredential(t *testing.T) {
	f := newRunnersFixture(t)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "previous", "--config", f.config, "--no-service"}); err != nil {
			t.Fatal(err)
		}
	})
	previous, err := agentconfig.Load(f.config)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(previous.CredentialsDir, "keep")
	if err := fssecure.WriteFile(marker, []byte("previous credential companion")); err != nil {
		t.Fatal(err)
	}
	f.failExec["systemctl --user daemon-reload"] = "service unavailable"
	err = f.run([]string{"add", "--profile", "prod", "--runner", "replacement", "--config", f.config, "--force"})
	if err == nil {
		t.Fatal("service failure reported success")
	}
	current, err := agentconfig.Load(f.config)
	if err != nil || current.HolderPrefix != previous.HolderPrefix || current.Token != previous.Token || current.CredentialsDir != previous.CredentialsDir {
		t.Fatalf("previous enrollment was changed: %+v %v", current, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("previous credential directory was cleaned: %v", err)
	}
	if len(f.tokens) != 2 || f.tokens[0].RevokedAt != nil || f.tokens[1].RevokedAt == nil {
		t.Fatalf("wrong authority revoked: %+v", f.tokens)
	}
	if dirs, err := filepath.Glob(filepath.Join(filepath.Dir(f.config), "runner-credentials-*")); err != nil || len(dirs) != 1 || dirs[0] != previous.CredentialsDir {
		t.Fatalf("new credential directory survived rollback: %v %v", dirs, err)
	}
}

func TestRunnerEnrollmentRevocationFailureReportsLiveAuthorityAndCleansNewFiles(t *testing.T) {
	f := newRunnersFixture(t)
	f.failExec["systemctl --user daemon-reload"] = "service unavailable"
	f.revokeErr = errors.New("controller unavailable")
	var enrollErr error
	out := captureStderr(t, func() {
		enrollErr = f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config})
	})
	if !errors.Is(enrollErr, f.revokeErr) || !strings.Contains(enrollErr.Error(), "service unavailable") {
		t.Fatalf("lost enrollment or revocation failure: %v", enrollErr)
	}
	if len(f.tokens) != 1 || f.tokens[0].RevokedAt != nil || !strings.Contains(out, "sparkwing tokens revoke --profile prod --token "+f.tokens[0].Prefix) {
		t.Fatalf("live authority has no revoke hint: %s %+v", out, f.tokens)
	}
	if cfg, err := userconfig.ReadConfig(f.config); err != nil || cfg.Runner != nil {
		t.Fatalf("failed enrollment config survived: %+v %v", cfg, err)
	}
	if dirs, err := filepath.Glob(filepath.Join(filepath.Dir(f.config), "runner-credentials-*")); err != nil || len(dirs) != 0 {
		t.Fatalf("failed enrollment credential survived: %v %v", dirs, err)
	}
}

func TestRunnerEnrollmentConfigWriteFailureCleansNewFilesAndKeepsExistingConfig(t *testing.T) {
	f := newRunnersFixture(t)
	body := "runner:\n  controller: https://controller.example\n  holder_prefix: previous\n  labels: &languages [LANG]\nrun:\n  submit_env_allow: *languages\n"
	if err := fssecure.WriteFile(f.config, []byte(body)); err != nil {
		t.Fatal(err)
	}
	err := f.run([]string{"add", "--profile", "prod", "--runner", "replacement", "--config", f.config, "--force", "--no-service"})
	if err == nil || !strings.Contains(err.Error(), "alias elsewhere") {
		t.Fatalf("expected config write failure: %v", err)
	}
	current, err := os.ReadFile(f.config)
	if err != nil || string(current) != body {
		t.Fatalf("failed config write changed previous file: %q %v", current, err)
	}
	if len(f.tokens) != 1 || f.tokens[0].RevokedAt == nil {
		t.Fatalf("failed config write left authority live: %+v", f.tokens)
	}
	if dirs, err := filepath.Glob(filepath.Join(filepath.Dir(f.config), "runner-credentials-*")); err != nil || len(dirs) != 0 {
		t.Fatalf("failed config write left new files: %v %v", dirs, err)
	}
}

func TestRunnerEnrollmentRaceRevokesNewAuthorityAndPreservesConcurrentConfig(t *testing.T) {
	f := newRunnersFixture(t)
	body := "runner:\n  controller: https://controller.example\n  holder_prefix: concurrent\n"
	f.afterMint = func() {
		if err := fssecure.WriteFile(f.config, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--config", f.config, "--no-service"})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("concurrent enrollment was overwritten: %v", err)
	}
	current, err := os.ReadFile(f.config)
	if err != nil || string(current) != body {
		t.Fatalf("concurrent config was changed: %q %v", current, err)
	}
	if len(f.tokens) != 1 || f.tokens[0].RevokedAt == nil {
		t.Fatalf("concurrent enrollment failure left authority live: %+v", f.tokens)
	}
}

func TestRunnerEnrollmentRollbackPreservesConcurrentReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	written, err := writeRunnerConfig(path, userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), HolderPrefix: preferencePointer("original"), MaxConcurrent: preferencePointer(1)}, "original-private-token", false)
	if err != nil {
		t.Fatal(err)
	}
	concurrent, err := writeRunnerConfig(path, userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), HolderPrefix: preferencePointer("replacement"), MaxConcurrent: preferencePointer(1)}, "replacement-private-token", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := written.rollback(); err != nil {
		t.Fatal(err)
	}
	current, err := agentconfig.Load(path)
	if err != nil || current.CredentialsDir != concurrent.credentialsDir || current.Token != "replacement-private-token" {
		t.Fatalf("concurrent replacement did not survive rollback: %+v %v", current, err)
	}
	if _, err := os.Stat(written.credentialsDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original failed enrollment credential was retained: %v", err)
	}
}

func TestRunnerEnrollmentRollbackPreservesReferencedCredentialWhenRestoreFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	written, err := writeRunnerConfig(path, userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), HolderPrefix: preferencePointer("original"), MaxConcurrent: preferencePointer(1)}, "original-private-token", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := written.rollback(); err == nil {
		t.Fatal("rollback reported success when the config lock was unavailable")
	}
	current, err := agentconfig.Load(path)
	if err != nil || current.Token != "original-private-token" {
		t.Fatalf("failed restoration deleted the referenced credential: %+v %v", current, err)
	}
}
