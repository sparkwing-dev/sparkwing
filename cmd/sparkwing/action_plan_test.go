//go:build unix

package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

const (
	fixtureDescribe = `"ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"env","type":"string","required":true},{"name":"fast","type":"bool","required":false}]}]}}`
	fixturePlan     = `"ok":true,"result":{"jobs":[{"id":"build","steps":[{"id":"lint","closures":{"skip_if":["build/step/lint/skip_if/0"]}},{"id":"compile","needs":["lint"]},{"id":"test","needs":["compile"]}],"failure_policy":"fail_fast"},{"id":"deploy-prod","deps":["build"],"retry":{"attempts":2},"timeout_ms":60000,"steps":[{"id":"run"}]}]}}`
	fixtureEval     = `"ok":true,"result":{"value":true}}`
)

func fakePlanSDK(t *testing.T, describe, plan, eval string) (bin, requests string) {
	t.Helper()
	dir := t.TempDir()
	bin, requests = filepath.Join(dir, "pipeline"), filepath.Join(dir, "requests")
	for name, body := range map[string]string{"hello": `"ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"fixture"},"features":[]}}`, "describe": describe, "plan": plan, "eval": eval} {
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
	planned, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--env", "prod", "--fast"}, "plan")
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
  Job "build" [would_run]
    Step "lint" [may_skip (user_skipif)]
      reason: SkipIf is evaluated at run time
    Step "compile" [would_run]
      needs: lint
    Step "test" [would_run]
      needs: compile
  Job "deploy-prod" [would_run]
    Step "run" [would_run]
`
	if got != want {
		t.Fatalf("plan output:\n%s\nwant:\n%s", got, want)
	}
	reqs := readRequests(t, requests)
	for _, sent := range []string{
		`"op":"describe"`,
		`"op":"plan","pipeline":"demo","args":{"env":"prod","fast":"true"},"run":{"run_id":"plan","pipeline":"demo"}}`,
	} {
		if !strings.Contains(reqs, sent) {
			t.Fatalf("requests:\n%s\nlack %s", reqs, sent)
		}
	}
}

// safety: a skip_if predicate is customer code, so inspection must never send the eval that runs it.
func TestPlanNeverEvaluatesSkipIf(t *testing.T) {
	bin, requests := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	planned, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--env", "prod"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := previewPlan(planned, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if reqs := readRequests(t, requests); strings.Contains(reqs, `"op":"eval"`) {
		t.Fatalf("plan evaluated a closure:\n%s", reqs)
	}
	if lint := doc.Jobs[0].Work.Steps[0]; lint.Decision != "may_skip" || lint.SkipReason != "user_skipif" {
		t.Fatalf("lint = %+v, want may_skip from an unevaluated SkipIf", lint)
	}
	if doc.Jobs[0].Decision != "would_run" {
		t.Fatalf("build = %+v, want would_run", doc.Jobs[0])
	}
}

func TestPlanRangeSkipsStepsOutsideTheBounds(t *testing.T) {
	bin, _ := fakePlanSDK(t, fixtureDescribe, fixturePlan, `"ok":true,"result":{"value":false}}`)
	planned, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--env", "prod"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := previewPlan(planned, "compile", "")
	if err != nil {
		t.Fatal(err)
	}
	steps := doc.Jobs[0].Work.Steps
	if steps[0].SkipReason != "range_skip" || steps[0].SkipDetail != "upstream of --start-at=compile" || steps[1].Decision != "would_run" {
		t.Fatalf("steps = %+v", steps)
	}
	if _, err := previewPlan(planned, "compil", ""); err == nil || !strings.Contains(err.Error(), `did you mean "compile"?`) {
		t.Fatalf("err = %v, want a suggestion", err)
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
			plan:    fixturePlan,
			want:    `demo: invalid arguments: argument --env is required`,
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
			_, err := planOverProtocol(context.Background(), pipelineentry.Binary(bin), t.TempDir(), "demo", tc.args, "plan")
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) || isInvalidArgs(err) != tc.invalid {
				t.Fatalf("err = %v (invalid_args %v), want %q", err, isInvalidArgs(err), tc.want)
			}
		})
	}
}

func TestPlanStopsAtTheDeadline(t *testing.T) {
	bin, _ := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nwhile read -r line <&3; do :; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := planOverProtocol(ctx, pipelineentry.Binary(bin), t.TempDir(), "demo", nil, "plan"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
}

func TestPlanParserRetainsPipelineArgumentsAfterSeparator(t *testing.T) {
	parsed, help, err := parsePipelinePlanArgs([]string{"--pipeline", "deploy-prod", "--", "--region", "us-east", "--image", "release"})
	if err != nil || help || parsed.pipeline != "deploy-prod" || strings.Join(parsed.passthrough, " ") != "--region us-east --image release" {
		t.Fatalf("inspection arguments: %+v help=%v err=%v", parsed, help, err)
	}
}

func TestPlanInspectionMasksSecretArgumentsAndErrors(t *testing.T) {
	describe := `"ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"token","type":"string","required":true,"secret":true}]}]}}`
	secret := "private-inspection-token"
	for _, response := range []string{fixturePlan, `"ok":false,"error":{"code":"plan_failed","message":"` + secret + `","hint":"` + secret + `","details":{"value":"` + secret + `"}}}`} {
		bin, requests := fakePlanSDK(t, describe, response, fixtureEval)
		planned, err := planOverProtocol(t.Context(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--token", secret}, "plan")
		var output strings.Builder
		if err != nil {
			if writeErr := writeInvocationError(&output, err, func(w io.Writer, failure *apierr.Error) error {
				return json.NewEncoder(w).Encode(apierr.Envelope{Error: failure})
			}); writeErr != nil {
				t.Fatal(writeErr)
			}
		} else {
			preview, err := previewPlan(planned, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(&output).Encode(preview); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), "***") {
			t.Fatalf("secret visible or classification lost: %s", &output)
		}
		if !strings.Contains(readRequests(t, requests), `"token":"`+secret+`"`) {
			t.Fatal("SDK planning did not receive the execution value")
		}
	}
}

func TestInspectionHelpReportsIncompleteDiscovery(t *testing.T) {
	describe := `"ok":true,"result":{"pipelines":[{"name":"deploy","args":[{"name":"target","type":"string","required":true}]},{"name":"broken","args":[],"error":{"code":"sdk_error","message":"unknown-private-value"}}]}}`
	bin, _ := fakePlanSDK(t, describe, fixturePlan, fixtureEval)
	inspection, err := inspectionCommand(pipelineentry.Binary(bin), t.TempDir(), "deploy")
	if err != nil {
		t.Fatal(err)
	}
	_, err = inspection.presentation(t.Context())
	if err == nil || !strings.Contains(err.Error(), "pipeline discovery incomplete (details withheld: secrets not yet known)") || strings.Contains(err.Error(), "unknown-private-value") {
		t.Fatalf("help diagnostic = %v", err)
	}
}

func FuzzPlanInspectionDoesNotDisplaySecrets(f *testing.F) {
	f.Add([]byte("token"), false)
	f.Add([]byte("quotes\"\n"), true)
	f.Fuzz(func(t *testing.T, input []byte, fail bool) {
		if len(input) > 1024 {
			return
		}
		secret := "private-token-" + hex.EncodeToString(input)
		encoded := base64.StdEncoding.EncodeToString([]byte(secret))
		describe := `"ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"token","type":"string","required":true,"secret":true}]}]}}`
		response := fixturePlan
		if fail {
			response = `"ok":false,"error":{"code":"plan_failed","message":"` + secret + `","hint":"` + encoded + `","details":{"token":"` + encoded + `"}}}`
		}
		bin, _ := fakePlanSDK(t, describe, response, fixtureEval)
		planned, err := planOverProtocol(t.Context(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--token", secret}, "plan")
		var output strings.Builder
		if fail {
			if err == nil {
				t.Fatal("failed plan succeeded")
			}
			if err := writeInvocationError(&output, err, func(w io.Writer, failure *apierr.Error) error {
				return json.NewEncoder(w).Encode(apierr.Envelope{Error: failure})
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			preview, err := previewPlan(planned, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(&output).Encode(preview); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(output.String(), secret) || strings.Contains(output.String(), encoded) {
			t.Fatalf("classified value escaped: %s", &output)
		}
	})
}

func TestPlanPreviewUsesJobVocabulary(t *testing.T) {
	bin, _ := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	planned, err := planOverProtocol(t.Context(), pipelineentry.Binary(bin), t.TempDir(), "demo", []string{"--env", "prod"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := previewPlan(planned, "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if _, found := value["jobs"]; !found {
		t.Fatal("plan preview omitted jobs")
	}
	if _, found := value["nodes"]; found {
		t.Fatal("plan preview exposed removed nodes field")
	}
}
