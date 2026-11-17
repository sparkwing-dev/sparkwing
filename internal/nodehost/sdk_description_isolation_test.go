package nodehost

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestSDKShortSecretArgumentsAndDefaultsStayUnmaskedWithoutWarning(t *testing.T) {
	for _, kind := range []string{"default", "supplied", "list"} {
		t.Run(kind, func(t *testing.T) {
			planner, input, fixture, _ := sdkPlanningFixture(t)
			var logs strings.Builder
			planner.Log = func(_ context.Context, frame nodewire.FrameLog) error { logs.WriteString(frame.Message); return nil }
			argument := nodewire.ArgumentDescription{Name: "token", Type: "string", Secret: true}
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
			planner.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				if err != nil {
					return process, err
				}
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, request nodewire.Request) (any, error) {
					if request.Op == nodewire.OpDescribe {
						return nodewire.DescribeDoc{Pipelines: []nodewire.PipelineDescription{{Name: input.Run.Pipeline, Args: []nodewire.ArgumentDescription{argument}}}}, nil
					}
					return answer(ctx, request)
				}
				return process, nil
			}
			prepared, err := planner.Prepare(t.Context(), input, fixture.input)
			if err != nil || prepared.Args["token"] == "" {
				t.Fatalf("short secret refused: %+v %v", prepared, err)
			}
			if strings.Contains(logs.String(), secrets.ShortValueWarning) {
				t.Fatalf("unexpected warning: %s", &logs)
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
			healthy := nodewire.PipelineDescription{Name: "build", Args: []nodewire.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &secret}}, Secrets: []nodewire.SecretDeclaration{{Name: "HEALTHY_TOKEN"}}}
			broken := nodewire.PipelineDescription{Name: "broken", Args: []nodewire.ArgumentDescription{}, Error: &apierr.Error{Code: apierr.PlanFailed, Message: "describe pipeline broken: missing deployment setting"}}
			entries := []nodewire.PipelineDescription{healthy, broken}
			if reversed {
				entries[0], entries[1] = entries[1], entries[0]
			}
			var logs []string
			var failurePhase string
			p.Log = func(_ context.Context, log nodewire.FrameLog) error { logs = append(logs, log.Message); return nil }
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodewire.Request) (any, error) {
					if req.Op == nodewire.OpDescribe || req.Op == nodewire.OpPlan {
						if err := log(ctx, nodewire.FrameLog{Message: unclassified}); err != nil {
							return nil, err
						}
					}
					if req.Op == nodewire.OpPlan && failurePhase == "plan" {
						return nil, errors.New(unclassified)
					}
					if req.Op == nodewire.OpDescribe {
						return nodewire.DescribeDoc{Pipelines: entries}, nil
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
				var plan nodewire.PlanDoc
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
				if !errors.As(err, &failure) || failure.Code != apierr.PlanFailed || failure.Message != "describe failed: plan_failed (details withheld: secrets not yet known); to see the details locally, run `sparkwing pipeline describe --pipeline "+input.Run.Pipeline+" --show-withheld`" || prepared.Plan != nil {
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

func TestWithheldPlanningOutputExplainsIncompleteDiscovery(t *testing.T) {
	const private = "private-describe-output"
	for _, tc := range []struct {
		code apierr.Code
		want string
	}{
		{apierr.IsolatedHomeRequired, `1 registered pipeline failed discovery (isolated_home_required), so its secret declarations are unknown; to see this output, retry with HOME="$(mktemp -d)" SPARKWING_HOME="$(mktemp -d)" before the sparkwing command`},
		{apierr.PlanFailed, "1 registered pipeline failed discovery (plan_failed), so its secret declarations are unknown; to see this output and its errors, run `sparkwing pipeline describe --pipeline build --show-withheld`"},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			p, input, fixture, _ := sdkPlanningFixture(t)
			input.Run.Pipeline = "build"
			var logs []string
			p.Log = func(_ context.Context, log nodewire.FrameLog) error { logs = append(logs, log.Message); return nil }
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodewire.Request) (any, error) {
					if req.Op == nodewire.OpDescribe || req.Op == nodewire.OpPlan {
						if err := log(ctx, nodewire.FrameLog{Message: private}); err != nil {
							return nil, err
						}
					}
					if req.Op == nodewire.OpDescribe {
						return nodewire.DescribeDoc{Pipelines: []nodewire.PipelineDescription{
							{Name: "build", Args: []nodewire.ArgumentDescription{}},
							{Name: "release", Args: []nodewire.ArgumentDescription{}, Error: &apierr.Error{Code: tc.code, Message: private}},
						}}, nil
					}
					return answer(ctx, req)
				}
				return process, err
			}
			prepared, err := p.Prepare(t.Context(), input, fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prepared.Plan(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := "sparkwing: output withheld because secret declarations were unavailable (1 records): " + tc.want
			explained := 0
			for _, log := range logs {
				if log == want {
					explained++
				}
			}
			if explained != 2 || slices.ContainsFunc(logs, func(log string) bool { return strings.Contains(log, private) }) {
				t.Fatalf("withheld output was not explained or leaked: %q", logs)
			}
		})
	}
}

func TestShowWithheldPrintsMaskedDiscoveryFailuresOnlyWhenRequested(t *testing.T) {
	const (
		cause        = "missing deploy target"
		output       = "describe progress"
		secret       = "healthy-private-default-value"
		localSecret  = "declared-local-secret-value"
		revealedHint = "sparkwing pipeline describe --pipeline release --show-withheld"
	)
	for _, tc := range []struct {
		selected string
		show     bool
	}{{"release", false}, {"release", true}, {"build", false}, {"build", true}} {
		t.Run(fmt.Sprintf("%s/show=%t", tc.selected, tc.show), func(t *testing.T) {
			p, input, _, _ := sdkPlanningFixture(t)
			p.ShowWithheld = tc.show
			input.Run.Pipeline = tc.selected
			defaultValue := secret
			if err := p.Masker.Register(localSecret); err != nil {
				t.Fatal(err)
			}
			var logs []string
			p.Log = func(_ context.Context, log nodewire.FrameLog) error { logs = append(logs, log.Message); return nil }
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodewire.Request) (any, error) {
					if req.Op != nodewire.OpDescribe {
						return answer(ctx, req)
					}
					if err := log(ctx, nodewire.FrameLog{Message: output + " " + secret}); err != nil {
						return nil, err
					}
					return nodewire.DescribeDoc{Pipelines: []nodewire.PipelineDescription{
						{Name: "build", Args: []nodewire.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &defaultValue}}},
						{Name: "release", Args: []nodewire.ArgumentDescription{}, Error: &apierr.Error{Code: apierr.PlanFailed, Message: cause + " near " + secret + " and " + localSecret}},
					}}, nil
				}
				return process, err
			}
			_, err := p.Inspect(t.Context(), input)
			all := strings.Join(logs, "\n")
			if err != nil {
				all += "\n" + err.Error()
				var failure *apierr.Error
				if errors.As(err, &failure) {
					all += "\n" + failure.Hint
				}
			}
			if strings.Contains(all, secret) || strings.Contains(all, localSecret) {
				t.Fatalf("registered secret escaped: %s", all)
			}
			if tc.show != strings.Contains(all, cause) || tc.show != strings.Contains(all, output) {
				t.Fatalf("withheld text shown=%t, want %t: %s", strings.Contains(all, cause), tc.show, all)
			}
			if !tc.show && strings.Count(all, "--show-withheld") != 1 {
				t.Fatalf("withheld remedy appears %d times, want once: %s", strings.Count(all, "--show-withheld"), all)
			}
			if !tc.show && !strings.Contains(all, "sparkwing pipeline describe --pipeline "+tc.selected+" --show-withheld") {
				t.Fatalf("withheld output does not name the command that shows it: %s", all)
			}
			if tc.show && strings.Contains(all, revealedHint) {
				t.Fatalf("shown output still points at the reveal command: %s", all)
			}
			if failed := tc.selected == "release"; failed != (err != nil) {
				t.Fatalf("selected %s returned %v", tc.selected, err)
			}
		})
	}
}

func TestShowWithheldPrintsOutputOfAnSDKThatStoppedBeforeDescribing(t *testing.T) {
	const crash = "SyntaxError: Unexpected token"
	for _, tc := range []struct {
		op   nodewire.Op
		show bool
	}{{nodewire.OpHello, false}, {nodewire.OpHello, true}, {nodewire.OpDescribe, false}, {nodewire.OpDescribe, true}} {
		show := tc.show
		t.Run(fmt.Sprintf("%s/show=%t", tc.op, show), func(t *testing.T) {
			p, input, _, _ := sdkPlanningFixture(t)
			p.ShowWithheld = show
			var logs []string
			p.Log = func(_ context.Context, log nodewire.FrameLog) error { logs = append(logs, log.Message); return nil }
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodewire.Request) (any, error) {
					if req.Op != tc.op {
						return answer(ctx, req)
					}
					if err := log(ctx, nodewire.FrameLog{Level: "error", Message: crash}); err != nil {
						return nil, err
					}
					return nil, executorError(hosterr.SDKExited, "SDK process exited: "+crash)
				}
				return process, err
			}
			_, err := p.Inspect(t.Context(), input)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != hosterr.SDKExited {
				t.Fatalf("describe error = %v", err)
			}
			all := strings.Join(logs, "\n") + "\n" + failure.Message
			if show != strings.Contains(all, crash) {
				t.Fatalf("crash output shown=%t, want %t: %s", strings.Contains(all, crash), show, all)
			}
			notice := "planning failed; see the error for the local details command"
			if !show && !slices.ContainsFunc(logs, func(log string) bool { return strings.HasSuffix(log, notice) }) {
				t.Fatalf("withheld notice does not point to the error: %q", logs)
			}
		})
	}
}
