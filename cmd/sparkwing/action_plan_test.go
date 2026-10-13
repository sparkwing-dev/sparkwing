//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

const (
	fixtureDescribe = `"ok":true,"result":{"protocol":"1","pipelines":[{"name":"demo","args":[{"name":"env","go_name":"Env","type":"string","required":true},{"name":"fast","go_name":"Fast","type":"bool"}]}]}}`
	fixturePlan     = `"ok":true,"result":{"protocol":"1","pipeline":"demo","run_id":"plan","nodes":[{"id":"build","deps":[],"work":{"steps":[{"id":"lint","has_skip_if":true,"closures":{"skip_if":["build/step/lint/skip_if/0"]}},{"id":"compile","needs":["lint"]},{"id":"test","needs":["compile"]}],"failure_policy":"fail-fast"}},{"id":"deploy-prod","deps":["build"],"modifiers":{"retry":2,"timeout_ms":60000},"work":{"steps":[{"id":"run"}]}}]}}`
	fixtureEval     = `"ok":true,"result":{"value":true}}`
)

func fakePlanSDK(t *testing.T, describe, plan, eval string) (bin, requests string) {
	t.Helper()
	dir := t.TempDir()
	bin, requests = filepath.Join(dir, "pipeline"), filepath.Join(dir, "requests")
	for name, body := range map[string]string{"describe": describe, "plan": plan, "eval": eval} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
[ "$#" = 1 ] && [ "$1" = --sw-node-protocol ] || exit 9
while read -r line <&3; do
  printf '%s\n' "$line" >> ` + requests + `
  id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
  op=$(printf '%s' "$line" | sed 's/.*"op":"\([^"]*\)".*/\1/')
  { printf '{"reply":"%s",' "$id"; cat ` + dir + `/$op; printf '\n'; } >&4
done
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, requests
}

func readRequests(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPlanRendersThePlanDocument(t *testing.T) {
	bin, requests := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	planned, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--env", "prod", "--fast"}, "plan", true)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := previewPlan(planned, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	got := captureStdout(t, func() { printPlanPreview(&doc) })
	want := `Plan: demo
Range: --start-at=- --stop-at=test
Args:
  env=prod
  fast=true
  Node "build" [would_run]
    Step "lint" [would_skip (user_skipif)]
    Step "compile" [would_run]
      needs: lint
    Step "test" [would_run]
      needs: compile
  Node "deploy-prod" [would_run]
    Step "run" [would_run]
`
	if got != want {
		t.Fatalf("plan output:\n%s\nwant:\n%s", got, want)
	}
	reqs := readRequests(t, requests)
	for _, sent := range []string{
		`{"id":"1","op":"describe"}`,
		`{"id":"2","op":"plan","pipeline":"demo","args":{"env":"prod","fast":"true"},"run":{"run_id":"plan"}}`,
		`{"id":"3","op":"eval","closure":"build/step/lint/skip_if/0"}`,
	} {
		if !strings.Contains(reqs, sent) {
			t.Fatalf("requests:\n%s\nlack %s", reqs, sent)
		}
	}
}

func TestPlanRangeSkipsStepsOutsideTheBounds(t *testing.T) {
	bin, _ := fakePlanSDK(t, fixtureDescribe, fixturePlan, `"ok":true,"result":{"value":false}}`)
	planned, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--env", "prod"}, "plan", true)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := previewPlan(planned, "compile", "")
	if err != nil {
		t.Fatal(err)
	}
	steps := doc.Nodes[0].Work.Steps
	if steps[0].SkipReason != "range_skip" || steps[0].SkipDetail != "upstream of --start-at=compile" || steps[1].Decision != "would_run" {
		t.Fatalf("steps = %+v", steps)
	}
	if _, err := previewPlan(planned, "compil", ""); err == nil || !strings.Contains(err.Error(), `did you mean "compile"?`) {
		t.Fatalf("err = %v, want a suggestion", err)
	}
}

func TestExplainRendersThePlanDocument(t *testing.T) {
	bin, requests := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	planned, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--env=prod"}, "explain", false)
	if err != nil {
		t.Fatal(err)
	}
	got := captureStdout(t, func() { printPlanSnapshot(&planned.doc) })
	want := `Plan: demo
  Node "build"
    Work
      Step "lint" [skip_if]
      Step "compile"  needs: lint
      Step "test"  needs: compile
  Node "deploy-prod" (Retry=2 Timeout=1m0s)
    Work
      Step "run"

Plan edges:
  build -> deploy-prod
`
	if got != want {
		t.Fatalf("explain output:\n%s\nwant:\n%s", got, want)
	}
	if reqs := readRequests(t, requests); strings.Contains(reqs, `"op":"eval"`) {
		t.Fatalf("explain evaluated closures:\n%s", reqs)
	}
}

func TestPlanErrorsNameTheirCause(t *testing.T) {
	for name, tc := range map[string]struct {
		plan    string
		args    []string
		want    string
		invalid bool
	}{
		"invalid_args": {
			plan:    `"ok":false,"error":{"code":"invalid_args","message":"inputs for pipeline \"demo\": --env is required"}}`,
			want:    `demo: invalid arguments: inputs for pipeline "demo": --env is required`,
			invalid: true,
		},
		"unknown flag": {
			plan:    fixturePlan,
			args:    []string{"--bogus", "x"},
			want:    "demo: invalid arguments: unknown flag --bogus",
			invalid: true,
		},
		"plan_failed": {
			plan: `"ok":false,"error":{"code":"plan_failed","message":"env boom cannot be planned"}}`,
			args: []string{"--env", "boom"},
			want: "demo: the pipeline's Plan failed: env boom cannot be planned",
		},
	} {
		t.Run(name, func(t *testing.T) {
			bin, _ := fakePlanSDK(t, fixtureDescribe, tc.plan, fixtureEval)
			_, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", tc.args, "plan", true)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) || isInvalidArgs(err) != tc.invalid {
				t.Fatalf("err = %v (invalid_args %v), want %q", err, isInvalidArgs(err), tc.want)
			}
		})
	}
}

func TestPlanStopsAtTheDeadline(t *testing.T) {
	bin, _ := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := planOverProtocol(ctx, pipelineentry.Binary(bin), t.TempDir(), "demo", nil, "plan", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
}
