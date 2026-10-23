package nodehost

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestSDKShortSecretArgumentsAndDefaultsWarnWithoutMasking(t *testing.T) {
	for _, kind := range []string{"default", "supplied", "list"} {
		t.Run(kind, func(t *testing.T) {
			planner, input, fixture, _ := sdkPlanningFixture(t)
			var logs strings.Builder
			planner.Log = func(_ context.Context, frame nodeproto.FrameLog) error { logs.WriteString(frame.Message); return nil }
			argument := nodeproto.ArgumentDescription{Name: "token", Type: "string", Secret: true}
			value := "abc"
			if kind == "default" {
				argument.Default = &value
			} else {
				input.Args = map[string]string{"token": value}
			}
			if kind == "list" {
				argument.Type = "[]string"
				input.Args["token"] = "abc,long-token"
			}
			start := planner.Start
			planner.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				if err != nil {
					return process, err
				}
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpDescribe {
						return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: input.Run.Pipeline, Args: []nodeproto.ArgumentDescription{argument}}}}, nil
					}
					return answer(ctx, request)
				}
				return process, nil
			}
			prepared, err := planner.Prepare(t.Context(), input, fixture.input)
			if err != nil || prepared.Args["token"] == "" {
				t.Fatalf("short secret refused: %+v %v", prepared, err)
			}
			if !strings.Contains(logs.String(), secrets.ShortValueWarning) {
				t.Fatalf("missing warning: %s", &logs)
			}
			if got := planner.Masker.Mask(value); got != value {
				t.Fatalf("short value masked: %q", got)
			}
		})
	}
}

func FuzzSDKDescribeIsolatesFailuresAndSecretDeclarations(f *testing.F) {
	const unclassified = "unknown-secret-from-broken-declaration"
	f.Add("TOKEN", false)
	f.Add("private\x00\n秘密", true)
	f.Fuzz(func(t *testing.T, value string, reversed bool) {
		secret := "private-" + hex.EncodeToString([]byte(value))
		for _, selected := range []string{"build", "broken"} {
			p, input, fixture, processes := sdkPlanningFixture(t)
			input.Run.Pipeline = selected
			healthy := nodeproto.PipelineDescription{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &secret}}, Secrets: []nodeproto.SecretDeclaration{{Name: "HEALTHY_TOKEN"}}}
			broken := nodeproto.PipelineDescription{Name: "broken", Args: []nodeproto.ArgumentDescription{}, Error: &apierr.Error{Code: apierr.PlanFailed, Message: "describe pipeline broken: missing deployment setting"}}
			entries := []nodeproto.PipelineDescription{healthy, broken}
			if reversed {
				entries[0], entries[1] = entries[1], entries[0]
			}
			var logs []string
			var failurePhase string
			p.Log = func(_ context.Context, log nodeproto.FrameLog) error { logs = append(logs, log.Message); return nil }
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodeproto.Request) (any, error) {
					if req.Op == nodeproto.OpDescribe || req.Op == nodeproto.OpPlan {
						if err := log(ctx, nodeproto.FrameLog{Message: unclassified}); err != nil {
							return nil, err
						}
					}
					if req.Op == nodeproto.OpPlan && failurePhase == "plan" {
						return nil, errors.New(unclassified)
					}
					if req.Op == nodeproto.OpDescribe {
						return nodeproto.DescribeDoc{Pipelines: entries}, nil
					}
					return answer(ctx, req)
				}
				return process, err
			}
			prepared, err := p.Prepare(t.Context(), input, fixture.input)
			if selected == "build" {
				if err != nil || len(prepared.Description.Secrets) != 1 || prepared.Description.Secrets[0].Name != "HEALTHY_TOKEN" || prepared.Args["token"] != secret || len(prepared.SecretArgs) != 1 {
					t.Fatalf("healthy pipeline lost its complete declarations: %+v: %v", prepared, err)
				}
				raw, err := prepared.Plan(t.Context())
				var plan nodeproto.PlanDoc
				if err != nil || json.Unmarshal(raw, &plan) != nil || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "job" {
					t.Fatalf("healthy pipeline did not plan: %s: %v", raw, err)
				}
				for _, phase := range []string{"plan", "declaration", "malformed"} {
					failurePhase = phase
					var err error
					if phase == "plan" {
						var result json.RawMessage
						result, err = prepared.Plan(t.Context())
						if len(result) != 0 {
							t.Fatal("failed planning returned plan bytes")
						}
					} else {
						// Both malformed JSON and invalid declarations must withhold unknown author text.
						body := json.RawMessage(`{"jobs":[{"id":"` + unclassified + `","unknown":true}]}`)
						if phase == "malformed" {
							body = json.RawMessage(`{"jobs":` + unclassified)
						}
						_, err = planInitial(t.Context(), prepared.Input, func(context.Context) (json.RawMessage, error) { return body, nil })
					}
					var failure *apierr.Error
					if !errors.As(err, &failure) || strings.Contains(failure.Message, unclassified) || !strings.Contains(failure.Message, "details withheld: secrets not yet known") {
						t.Fatalf("partial describe released unclassified %s failure: %v", phase, err)
					}
				}
			} else {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.PlanFailed || failure.Message != "describe failed: plan_failed (details withheld: secrets not yet known)" || prepared.Plan != nil {
					t.Fatalf("broken pipeline did not withhold unclassified failure details: %+v: %v", prepared, err)
				}
			}
			for _, log := range logs {
				if strings.Contains(log, "unknown-secret") {
					t.Fatalf("partial describe released unclassified output: %s", log)
				}
			}
			for _, process := range *processes {
				if process.closed != 1 || process.cleaned != 1 {
					t.Fatal("describe did not join cleanup")
				}
			}
		}
	})
}
