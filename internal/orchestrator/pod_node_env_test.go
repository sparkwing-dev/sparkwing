package orchestrator

import (
	"slices"
	"testing"
)

func TestOperatorAllowedEnvPassesListedNamesButNeverEngineOrCredentialNames(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [AWS_PROFILE, AWS_REGION, DOCKER_*, PATH, SPARKWING_AGENT_TOKEN, SPARKWING_NODE_CLAIM_*, GITHUB_PAT]\n")
	env := []string{
		"AWS_PROFILE=ci",
		"AWS_REGION=us-east-1",
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"AWS_SECRET_ACCESS_KEY=unlisted",
		"KUBECONFIG=/unlisted",
		"PATH=/runner/bin",
		"SPARKWING_AGENT_TOKEN=swr_runner",
		"SPARKWING_NODE_CLAIM_HOLDER=pod",
		"GITHUB_PAT=ghp_live",
	}
	got, err := operatorAllowedEnv(env, []string{"PATH=/usr/bin"}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"AWS_PROFILE=ci", "AWS_REGION=us-east-1", "DOCKER_HOST=unix:///var/run/docker.sock"}
	if !slices.Equal(got, want) {
		t.Fatalf("node env = %#v, want %#v", got, want)
	}
}

func TestOperatorAllowedEnvRefusesABareWildcard(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [\"*\"]\n")
	if _, err := operatorAllowedEnv([]string{"AWS_PROFILE=ci"}, nil, quietLogger()); err == nil {
		t.Fatal("operatorAllowedEnv error = nil, want the bare wildcard refused")
	}
}
