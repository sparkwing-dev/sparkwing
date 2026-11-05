package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func retryWithEnv(t *testing.T, env map[string]string, args ...string) ([]controllerwire.RetryRequest, error) {
	t.Helper()
	var sent []controllerwire.RetryRequest
	c := retryController{retry: func(_ context.Context, _ string, input controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
		sent = append(sent, input)
		return retryResponse("new"), nil
	}}
	resolve := func(context.Context, string) (retryConnection, error) {
		return retryConnection{Controller: c, close: func() {}}, nil
	}
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "retry", "--run", "source", "--failed", "-o", "plain"}, args...))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = runRunsRetryCommand(t.Context(), inv, strings.NewReader(""), &out, func(name string) string { return env[name] }, resolve)
	return sent, err
}

func TestAgentRetryWithoutReasonIsRefusedBeforeConnecting(t *testing.T) {
	for _, env := range []map[string]string{{"CLAUDECODE": "1"}, {"CODEX_THREAD_ID": "t"}, {"GEMINI_CLI": "1"}, {agentEnv: "lane"}} {
		sent, err := retryWithEnv(t, env)
		if exitCodeFor(err) != 2 || len(sent) != 0 || !strings.Contains(err.Error(), "--reason") {
			t.Fatalf("env %v: sent=%v err=%v", env, sent, err)
		}
	}
	if sent, err := retryWithEnv(t, nil, "--agent", "lead"); exitCodeFor(err) != 2 || len(sent) != 0 {
		t.Fatalf("--agent without reason: sent=%v err=%v", sent, err)
	}
}

func TestRetrySendsReasonAndDetectedAgent(t *testing.T) {
	sent, err := retryWithEnv(t, map[string]string{"CLAUDECODE": "1"}, "--reason", "fixed X in commit Y")
	if err != nil || len(sent) != 1 || sent[0].Agent != "claude-code" || sent[0].Reason != "fixed X in commit Y" {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
}

func TestHumanRetryNeedsNoReason(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		args []string
	}{{nil, nil}, {map[string]string{"CLAUDECODE": "1"}, []string{"--agent", runspec.AgentNone}}, {nil, []string{"--reason", "flake"}}} {
		sent, err := retryWithEnv(t, tc.env, tc.args...)
		if err != nil || len(sent) != 1 || sent[0].Agent != "" {
			t.Fatalf("env=%v args=%v: sent=%+v err=%v", tc.env, tc.args, sent, err)
		}
	}
}

func TestRunsStatusAndListShowRetryReason(t *testing.T) {
	detail := controllerwire.RunDetail{ID: "run-b", Status: "running", Inputs: runspec.Display{Pipeline: "deploy", Lineage: runspec.Lineage{RetryOf: "run-a", RetryReason: "flake: DNS timeout in TestX"}}}
	var status bytes.Buffer
	if err := writeRunDetail(&status, detail, runStatusOptions{format: "pretty"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), "retry_of\trun-a") || !strings.Contains(status.String(), "retry_reason\tflake: DNS timeout in TestX") {
		t.Fatalf("status output:\n%s", &status)
	}
	for _, format := range []string{"pretty", "json"} {
		var list bytes.Buffer
		if err := writeListedRun(&list, detail, format, false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(list.String(), "flake: DNS timeout in TestX") || !strings.Contains(list.String(), "run-a") {
			t.Fatalf("%s list output: %s", format, &list)
		}
	}
}
