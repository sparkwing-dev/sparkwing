package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/agentconfig"
	"github.com/sparkwing-dev/sparkwing/internal/agentservice"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerEnrollmentWritesFinalSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SPARKWING_CONFIG", path)
	cfg := agentFileConfig{Controller: "https://controller.example", Token: "private-runner-token", MaxConcurrent: 2, AllowRepos: []string{"github.com/acme/*"}}
	if err := validateAgentFileConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := finishRunnerEnrollment(path, cfg, false, runnerServicePlan{skipped: true}); err != nil {
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
	if err := writeAgentConfig(path, cfg, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("existing enrollment was not refused: %v", err)
	}
	runner, err = agentconfig.Load(path)
	if err != nil || runner.Token != previous {
		t.Fatalf("refused replacement changed the credential: %v", err)
	}
	if err := writeAgentConfig(path, cfg, true); err != nil {
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
	service := runnerServicePlan{host: agentservice.Host{
		GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir(), Binary: "/test/sparkwing-runner", ConfigPath: path,
		Exec: func(name string, args ...string) (string, error) {
			called = true
			return "fixture service failure", errors.New("service failed")
		},
	}}
	err := finishRunnerEnrollment(path, agentFileConfig{Controller: "https://controller.example", Token: "private-runner-token", MaxConcurrent: 2}, false, service)
	if !called || err == nil || !strings.Contains(err.Error(), "fixture service failure") {
		t.Fatalf("failure did not reach service installation: %v", err)
	}
	if _, err := userconfig.ReadConfig(path); err != nil {
		t.Fatalf("service failure left invalid configuration: %v", err)
	}
}

func TestRunnerEnrollmentRejectsBadRepoPattern(t *testing.T) {
	err := validateAgentFileConfig(agentFileConfig{Controller: "https://controller.example", MaxConcurrent: 2, AllowRepos: []string{"https://github.com/acme/*"}})
	if err == nil || !strings.Contains(err.Error(), "runner.repos") || !strings.Contains(err.Error(), "invalid repository allow-list") {
		t.Fatalf("expected repository pattern refusal: %v", err)
	}
}
