//go:build !windows

package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The pipeline binary a trigger runs is the team's own code. It starts with
// the run's own credentials and none of the launcher's: no operator cache
// token, no ambient cloud or database secret, no stale grant from another run.
func TestTriggerChildStartsWithOnlyTheRunsCredentials(t *testing.T) {
	launcher := map[string]string{
		"SPARKWING_CACHE_TOKEN":  operatorCacheToken,
		"SPARKWING_CACHE_GRANT":  "swcg1.another-runs-grant",
		"SPARKWING_API_TOKEN":    "operator-api-token",
		"AWS_SECRET_ACCESS_KEY":  "operator-aws-secret",
		"GITHUB_TOKEN":           "operator-github-token",
		"DATABASE_URL":           "postgres://sparkwing:operator-db-password@db/sparkwing",
		"LAUNCHER_AMBIENT":       "launcher-only-value",
		"SPARKWING_GITCACHE_URL": "http://cache.internal",
		"GOFLAGS":                "-mod=mod",
	}
	for name, value := range launcher {
		t.Setenv(name, value)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "pipeline")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nenv > child.env\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := TriggerLoopOptions{ControllerURL: "https://controller.example", LogsURL: "https://logs.example", Token: "runner-a"}
	if err := execHandleTrigger(context.Background(), script, dir, &store.Trigger{ID: "run-a"}, opts, "swcg1.run-a-grant", discardLogger()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "child.env"))
	if err != nil {
		t.Fatal(err)
	}
	child := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			child[name] = value
		}
	}

	if got, ok := child["SPARKWING_CACHE_TOKEN"]; ok {
		t.Errorf("SPARKWING_CACHE_TOKEN=%q reached the team's code", got)
	}
	secrets := []string{
		operatorCacheToken, "operator-api-token", "operator-aws-secret",
		"operator-github-token", "operator-db-password", "launcher-only-value", "another-runs-grant",
	}
	for _, secret := range secrets {
		for name, value := range child {
			if strings.Contains(value, secret) {
				t.Errorf("%s carries the launcher's %q into the team's code", name, secret)
			}
		}
	}
	want := map[string]string{
		"SPARKWING_CACHE_GRANT":    "swcg1.run-a-grant",
		"SPARKWING_AGENT_TOKEN":    "runner-a",
		"SPARKWING_CONTROLLER_URL": "https://controller.example",
		"SPARKWING_GITCACHE_URL":   "http://cache.internal",
		"GOFLAGS":                  "-mod=mod",
		"PATH":                     os.Getenv("PATH"),
	}
	for name, value := range want {
		if child[name] != value {
			t.Errorf("child %s = %q, want %q", name, child[name], value)
		}
	}
}
