package local

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// lastValue mirrors how os/exec resolves duplicates: the last entry
// for a key wins.
func lastValue(env []string, key string) (string, bool) {
	out, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			out, found = v, true
		}
	}
	return out, found
}

func testConfig() Config {
	return Config{
		Executable:    "/opt/pipeline",
		ControllerURL: "http://127.0.0.1:52341",
		AgentToken:    "svc_tok",
		WorkDir:       "/repo",
		Home:          "/home/dev/.sparkwing",
		CacheURL:      "fs:///home/dev/.sparkwing/cache",
		Labels:        []string{"local", "os=darwin"},
		DryRun:        true,
		StartAt:       "build",
		StopAt:        "deploy",
		LeaseTokens: func(context.Context) (string, string) {
			return "lease-abc", "child-def"
		},
	}
}

func TestChildEnv_PinsTheNodeContract(t *testing.T) {
	env := childEnv(context.Background(), nil, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})

	want := map[string]string{
		"SPARKWING_HOME":            "/home/dev/.sparkwing",
		"SPARKWING_CONTROLLER_URL":  "http://127.0.0.1:52341",
		"SPARKWING_CACHE_URL":       "fs:///home/dev/.sparkwing/cache",
		"SPARKWING_AGENT_TOKEN":     "svc_tok",
		"SPARKWING_RUN_ID":          "run-1",
		"SPARKWING_NODE_ID":         "build",
		"SPARKWING_LOG_FORMAT":      "json",
		"SPARKWING_RUNNER_NAME":     "local",
		"SPARKWING_RUNNER_TYPE":     "local",
		"SPARKWING_RUNNER_LABELS":   "local,os=darwin",
		"SPARKWING_DRY_RUN":         "1",
		"SPARKWING_START_AT":        "build",
		"SPARKWING_STOP_AT":         "deploy",
		wingwire.LeaseTokenEnv:      "lease-abc",
		wingwire.ChildLeaseTokenEnv: "child-def",
	}
	for k, v := range want {
		got, ok := lastValue(env, k)
		if !ok {
			t.Errorf("%s missing from child env", k)
			continue
		}
		if got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// The node body reads the operator's own environment -- credentials,
// PATH, toolchain settings -- so inheriting is the default and pinning
// is the exception.
func TestChildEnv_InheritsBaseAndOverridesPinnedKeys(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"AWS_PROFILE=work",
		"SPARKWING_CONTROLLER_URL=http://stale.example",
		"SPARKWING_LOG_FORMAT=pretty",
	}
	env := childEnv(context.Background(), base, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})

	if v, _ := lastValue(env, "PATH"); v != "/usr/bin" {
		t.Errorf("PATH = %q, want the inherited value", v)
	}
	if v, _ := lastValue(env, "AWS_PROFILE"); v != "work" {
		t.Errorf("AWS_PROFILE = %q, want the inherited value", v)
	}
	if v, _ := lastValue(env, "SPARKWING_CONTROLLER_URL"); v != "http://127.0.0.1:52341" {
		t.Errorf("SPARKWING_CONTROLLER_URL = %q, want this run's controller", v)
	}
	// safety: a pretty renderer would arrive on stdout as lines the dispatcher
	// cannot decode, so the operator does not get to choose here.
	if v, _ := lastValue(env, "SPARKWING_LOG_FORMAT"); v != "json" {
		t.Errorf("SPARKWING_LOG_FORMAT = %q, want json", v)
	}
}

// --logs= is passed on the command line; a logs URL in the
// environment must not be manufactured here, or a resident
// dashboard's service would collect this run's node logs.
func TestChildEnv_DoesNotSetLogsURL(t *testing.T) {
	env := childEnv(context.Background(), nil, testConfig(),
		runner.Request{RunID: "run-1", NodeID: "build"})
	if v, ok := lastValue(env, "SPARKWING_LOGS_URL"); ok {
		t.Errorf("SPARKWING_LOGS_URL = %q, want it unset", v)
	}
}

func TestChildEnv_OmitsEmptyAndUnavailableValues(t *testing.T) {
	cfg := Config{ControllerURL: "http://x", Labels: []string{"local"}}
	env := childEnv(context.Background(), nil, cfg,
		runner.Request{RunID: "run-1", NodeID: "build"})

	for _, k := range []string{
		"SPARKWING_CACHE_URL", "SPARKWING_AGENT_TOKEN", "SPARKWING_DRY_RUN",
		"SPARKWING_START_AT", "SPARKWING_STOP_AT",
		wingwire.LeaseTokenEnv, wingwire.ChildLeaseTokenEnv,
	} {
		if v, ok := lastValue(env, k); ok {
			t.Errorf("%s = %q, want it absent when unconfigured", k, v)
		}
	}
}
