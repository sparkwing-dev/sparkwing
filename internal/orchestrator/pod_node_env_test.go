package orchestrator

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestOperatorAllowedEnvPassesListedNamesButNeverEngineOrCredentialNames(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [AWS_PROFILE, AWS_REGION, DOCKER_*, PATH, SPARKWING_AGENT_TOKEN, SPARKWING_NODE_CLAIM_*, GITHUB_PAT, BUILD_NOTE]\n")
	env := []string{
		"AWS_PROFILE=ci",
		"AWS_REGION=us-east-1",
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"BUILD_NOTE=ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"AWS_SECRET_ACCESS_KEY=unlisted",
		"KUBECONFIG=/unlisted",
		"PATH=/runner/bin",
		"SPARKWING_AGENT_TOKEN=swr_runner",
		"SPARKWING_NODE_CLAIM_HOLDER=pod",
		"GITHUB_PAT=ghp_live",
	}
	var logged bytes.Buffer
	masker := secrets.NewMasker()
	got, err := operatorAllowedEnv(env, []string{"PATH=/usr/bin"}, masker, slog.New(slog.NewTextHandler(&logged, nil)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"AWS_PROFILE=ci", "AWS_REGION=us-east-1", "DOCKER_HOST=unix:///var/run/docker.sock",
		"BUILD_NOTE=ghp_0123456789abcdefghijklmnopqrstuvwxyz",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("node env = %#v, want %#v", got, want)
	}
	if line := masker.Mask("note ghp_0123456789abcdefghijklmnopqrstuvwxyz"); strings.Contains(line, "ghp_") {
		t.Fatalf("a passed-through value reached the log unmasked: %q", line)
	}
	for _, name := range []string{"SPARKWING_AGENT_TOKEN", "SPARKWING_NODE_CLAIM_HOLDER", "GITHUB_PAT"} {
		if !strings.Contains(logged.String(), name) {
			t.Errorf("drop of %s was not logged: %s", name, logged.String())
		}
	}
}

func TestOperatorAllowedEnvRefusesABareWildcard(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [\"*\"]\n")
	if _, err := operatorAllowedEnv([]string{"AWS_PROFILE=ci"}, nil, secrets.NewMasker(), quietLogger()); err == nil {
		t.Fatal("operatorAllowedEnv error = nil, want the bare wildcard refused")
	}
}
