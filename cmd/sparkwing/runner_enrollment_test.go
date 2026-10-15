package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/agentservice"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
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
	if _, err := finishRunnerEnrollment(path, cfg, bearer, false, runnerServicePlan{skipped: true}); err != nil {
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
	service := runnerServicePlan{host: agentservice.Host{
		GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir(), Binary: "/test/sparkwing-runner", ConfigPath: path,
		Exec: func(name string, args ...string) (string, error) {
			called = true
			return "fixture service failure", errors.New("service failed")
		},
	}}
	_, err := finishRunnerEnrollment(path, userconfig.RunnerConfig{Controller: preferencePointer("https://controller.example"), MaxConcurrent: preferencePointer(2)}, "private-runner-token", false, service)
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
