package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestSDKPlanningDescribesDefaultsBeforePlanning(t *testing.T) {
	f := newPlanningFixture(t)
	resolved := engine.Resolved{Identity: f.input.Inputs.Engine}
	var logs []nodeproto.FrameLog
	var processes []*executorProcess
	defaultValue, supplied := "sdk-private-default", "explicit-private-value"
	empty, fallback := "", "public-default"
	planner := SDKPlanner{
		Masker: secrets.NewMasker(),
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		},
		Log:          func(_ context.Context, record nodeproto.FrameLog) error { logs = append(logs, record); return nil },
		ValidatePlan: f.functions().Validate, CanonicalHash: f.functions().Hash,
	}
	planner.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		if planner.Masker.Mask(input.Args["label"]) != "***" {
			t.Fatal("intake secret classification was not registered before describe")
		}
		if input.Entry.Engine.Identity != resolved.Identity {
			t.Fatal("planning changed its selected engine")
		}
		process := &executorProcess{plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}}
		process.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			switch request.Op {
			case nodeproto.OpHello:
				if request.Binding != nil || !reflect.DeepEqual(request.Runner, &input.Runner) {
					t.Fatal("planning hello gained route authority or lost granted runner identity")
				}
				return nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: string(resolved.Identity.Pin.SDK), Version: resolved.Identity.Pin.Version}, Features: []string{}}, nil
			case nodeproto.OpDescribe:
				if err := log(ctx, nodeproto.FrameLog{Message: defaultValue + " " + supplied, Attrs: json.RawMessage(`{"sdk-private-def\u0061ult":"public"}`)}); err != nil {
					return nil, err
				}
				if len(logs) != 0 {
					t.Fatal("describe output escaped before secret classification")
				}
				return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{
					{Name: "other", Args: []nodeproto.ArgumentDescription{{Name: "other-secret", Secret: true, Default: &defaultValue}}},
					{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Secret: true}, {Name: "empty", Default: &empty}, {Name: "label", Default: &fallback}}},
				}}, nil
			case nodeproto.OpPlan:
				if request.Args["token"] != supplied || request.Args["empty"] != "" || request.Args["label"] != "explicit" || request.Run.RunID != "run" {
					t.Fatalf("planning inputs changed: %+v", request)
				}
				return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}, nil
			}
			return nil, nil
		}
		processes = append(processes, process)
		return process, nil
	}
	input := PlanningProcessInput{Entry: &source.Entry{Engine: resolved}, Runner: nodeproto.RunnerIdentity{Name: "granted", Type: "local"}, Run: nodeproto.RunContext{RunID: "run", Pipeline: "build"}, Args: map[string]string{"token": supplied, "label": "explicit"}, SecretArgs: []string{"label"}}
	prepared, err := planner.Prepare(t.Context(), input, f.input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.SecretArgs, []string{"label", "token"}) || prepared.Args["empty"] != "" || len(prepared.Args) != 3 {
		t.Fatalf("description = %+v", prepared)
	}
	if len(logs) != 1 || strings.Contains(logs[0].Message, "private") || strings.Contains(string(logs[0].Attrs), "private") {
		t.Fatalf("logs = %+v", logs)
	}
	if _, err := PlanInitial(t.Context(), prepared.Input, prepared.Functions); err != nil {
		t.Fatal(err)
	}
	if len(processes) != 2 {
		t.Fatalf("started %d processes", len(processes))
	}
	for _, p := range processes {
		if p.closed != 1 || p.cleaned != 1 {
			t.Fatal("planning process was not joined and cleaned")
		}
	}
}

func sdkPlanningFixture(t *testing.T) (SDKPlanner, PlanningProcessInput, *planningFixture, *[]*executorProcess) {
	t.Helper()
	f := newPlanningFixture(t)
	resolved := engine.Resolved{Identity: f.input.Inputs.Engine}
	processes := &[]*executorProcess{}
	input := PlanningProcessInput{Entry: &source.Entry{Engine: resolved}, Runner: nodeproto.RunnerIdentity{Name: "granted", Type: "local"}, Run: nodeproto.RunContext{RunID: "run", Pipeline: "build"}}
	p := SDKPlanner{
		Masker: secrets.NewMasker(), Log: func(context.Context, nodeproto.FrameLog) error { return nil },
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		},
		ValidatePlan: f.functions().Validate, CanonicalHash: f.functions().Hash,
		Start: func(_ context.Context, _ PlanningProcessInput, _ func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
			process := &executorProcess{plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}}}
			process.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
				switch request.Op {
				case nodeproto.OpHello:
					return nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: string(resolved.Identity.Pin.SDK), Version: resolved.Identity.Pin.Version}, Features: []string{}}, nil
				case nodeproto.OpDescribe:
					return nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "build", Args: []nodeproto.ArgumentDescription{}}}}, nil
				}
				return nil, nil
			}
			*processes = append(*processes, process)
			return process, nil
		},
	}
	return p, input, f, processes
}

func TestSDKPlanningCacheKeepsCapturedAndHostEnvironmentIdentity(t *testing.T) {
	seen := map[runspec.Hash]bool{}
	for _, captured := range []string{"original", "changed"} {
		for _, host := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), ""} {
			planner, input, fixture, _ := sdkPlanningFixture(t)
			seal, err := runspec.EnvironmentDigest(map[string]string{"CAPTURED": captured}, strings.Repeat("5", 64))
			if err != nil {
				t.Fatal(err)
			}
			input.Environment = seal
			fixture.input.Inputs.EnvironmentDigest = runspec.Hash(host)
			prepared, err := planner.Prepare(t.Context(), input, fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			digest := prepared.Input.Inputs.EnvironmentDigest
			if host == "" {
				if digest != "" {
					t.Fatal("snapshot identity hid missing host environment inputs")
				}
				continue
			}
			if seen[digest] {
				t.Fatal("planning cache ignored a changed snapshot or host environment")
			}
			seen[digest] = true
		}
	}
}

func TestSDKPlanningRetainsSealedRunContext(t *testing.T) {
	planner, input, fixture, processes := sdkPlanningFixture(t)
	input.Run.StartedAt = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	input.Run.DryRun, input.Run.NoMemo = true, true
	input.Run.Git = &nodeproto.Git{SHA: strings.Repeat("a", 40), Branch: "feature", DefaultBranch: "main", Repo: "acme/repo", RepoURL: "https://example.com/acme/repo.git"}
	input.Run.Trigger = &nodeproto.Trigger{Source: "webhook", User: "octocat", PullRequest: &nodeproto.PullRequest{Number: 42, Action: "synchronize", BaseRef: "main", HeadRef: "feature"}}
	sealed, err := json.Marshal(input.Run)
	if err != nil {
		t.Fatal(err)
	}
	start := planner.Start
	planned := false
	planner.Start = func(ctx context.Context, processInput PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		process, err := start(ctx, processInput, log)
		p := process.(*executorProcess)
		answer := p.answer
		p.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpPlan {
				planned = true
				actual, marshalErr := json.Marshal(request.Run)
				if marshalErr != nil || string(actual) != string(sealed) {
					t.Errorf("planner changed sealed context: %s (%v)", actual, marshalErr)
				}
			}
			return answer(ctx, request)
		}
		return p, err
	}
	prepared, err := planner.Prepare(t.Context(), input, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	input.Run.Git.SHA, input.Run.Trigger.User, input.Run.Trigger.PullRequest.Number = "later", "later", 99
	if _, err := PlanInitial(t.Context(), prepared.Input, prepared.Functions); err != nil {
		t.Fatal(err)
	}
	if !planned || len(*processes) != 2 {
		t.Fatal("context proof did not reach fresh SDK planning")
	}
	for _, process := range *processes {
		if process.closed != 1 || process.cleaned != 1 {
			t.Fatal("context proof leaked an SDK process")
		}
	}
}

func TestSDKPlanningCacheHitDoesNotStartAPlanProcess(t *testing.T) {
	p, input, f, processes := sdkPlanningFixture(t)
	for i := range 2 {
		prepared, err := p.Prepare(t.Context(), input, f.input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := PlanInitial(t.Context(), prepared.Input, prepared.Functions)
		if err != nil || result.CacheHit != (i == 1) {
			t.Fatalf("plan %d: %+v, %v", i, result, err)
		}
	}
	if len(*processes) != 3 || f.validations != 2 {
		t.Fatalf("processes = %d, validations = %d", len(*processes), f.validations)
	}
	for _, process := range *processes {
		if process.closed != 1 || process.cleaned != 1 {
			t.Fatal("cache hit leaked a process")
		}
	}
}

func TestSDKPlanningDescribeTimeoutJoinsAndWithholdsUnknownSecrets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, input, f, processes := sdkPlanningFixture(t)
		var logs []nodeproto.FrameLog
		p.Log = func(_ context.Context, record nodeproto.FrameLog) error { logs = append(logs, record); return nil }
		start := p.Start
		p.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
			process, err := start(ctx, input, log)
			proc := process.(*executorProcess)
			answer := proc.answer
			proc.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
				if request.Op == nodeproto.OpDescribe {
					if err := log(ctx, nodeproto.FrameLog{Message: "unknown-private-default"}); err != nil {
						return nil, err
					}
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return answer(ctx, request)
			}
			return process, err
		}
		before := time.Now()
		_, err := p.Prepare(t.Context(), input, f.input)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.DeadlineExceeded || strings.Contains(err.Error(), "private") {
			t.Fatalf("error = %v", err)
		}
		if time.Since(before) != 30*time.Second {
			t.Fatalf("describe budget = %v", time.Since(before))
		}
		if len(logs) != 1 || !strings.Contains(logs[0].Message, "withheld") || strings.Contains(logs[0].Message, "private") {
			t.Fatalf("logs = %+v", logs)
		}
		if (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
			t.Fatal("timeout did not join process cleanup")
		}
	})
}

func TestSDKPlanningRejectsIncompleteDescriptionsAndCleansUp(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"pipelines":null}`, `{"pipelines":[]}`,
		`{"pipelines":[{"name":"build","args":null}]}`,
		`{"pipelines":[{"name":"build","args":[]},{"name":"build","args":[]}]}`,
		`{"pipelines":[{"name":"build","args":[{"name":"x"},{"name":"x"}]}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			p, input, f, processes := sdkPlanningFixture(t)
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodeproto.Request) (any, error) {
					if req.Op == nodeproto.OpDescribe {
						return json.RawMessage(raw), nil
					}
					return answer(ctx, req)
				}
				return process, err
			}
			if _, err := p.Prepare(t.Context(), input, f.input); err == nil {
				t.Fatal("accepted incomplete description")
			}
			if (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
				t.Fatal("refused description leaked its process")
			}
		})
	}
}

func TestSDKPlanningFailuresDiscardPlanAndMaskErrors(t *testing.T) {
	for _, stage := range []string{"start", "hello", "describe", "plan", "close", "cleanup", "log", "validate", "hash"} {
		t.Run(stage, func(t *testing.T) {
			p, input, f, processes := sdkPlanningFixture(t)
			secret := "private-planning-secret"
			p.Masker.Register(secret)
			failure := errors.New(secret)
			start := p.Start
			p.Start = func(ctx context.Context, in PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, in, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, req nodeproto.Request) (any, error) {
					if string(req.Op) == stage {
						return nil, failure
					}
					if stage == "log" && req.Op == nodeproto.OpDescribe {
						if err := log(ctx, nodeproto.FrameLog{Message: secret}); err != nil {
							return nil, err
						}
					}
					return answer(ctx, req)
				}
				if stage == "start" {
					return process, failure
				}
				if stage == "close" {
					proc.closeErr = failure
				}
				if stage == "cleanup" {
					proc.cleanupErr = failure
				}
				return process, err
			}
			if stage == "log" {
				p.Log = func(context.Context, nodeproto.FrameLog) error { return failure }
			}
			if stage == "validate" {
				p.ValidatePlan = func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					return nodeproto.PlanDoc{}, failure
				}
			}
			if stage == "hash" {
				p.CanonicalHash = func(any) (runspec.Hash, error) { return "", failure }
			}
			prepared, err := p.Prepare(t.Context(), input, f.input)
			var result PlannedInitial
			if err == nil {
				result, err = PlanInitial(t.Context(), prepared.Input, prepared.Functions)
			}
			if err == nil || strings.Contains(err.Error(), secret) || len(result.CanonicalPlan) != 0 || len(f.cache.puts) != 0 {
				t.Fatalf("plan = %+v, error = %v", result, err)
			}
			for _, process := range *processes {
				if process.closed != 1 || process.cleaned != 1 {
					t.Fatal("failure leaked its process")
				}
			}
		})
	}
}

func TestSDKPlanningCacheIdentityRetainsPlatformAndCallerRunnerFacts(t *testing.T) {
	p, input, f, _ := sdkPlanningFixture(t)
	input.Entry.Engine.Target = releaseasset.Target{GOOS: "linux", GOARCH: "amd64"}
	p.EnsureEngine = func(_ context.Context, identity engine.Identity, target releaseasset.Target) (engine.Resolved, error) {
		return engine.Resolved{Identity: identity, Target: target}, nil
	}
	first, err := p.Prepare(t.Context(), input, f.input)
	if err != nil {
		t.Fatal(err)
	}
	input.Entry.Engine.Target.GOARCH = "arm64"
	second, err := p.Prepare(t.Context(), input, f.input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Input.Inputs.RunnerDigest == second.Input.Inputs.RunnerDigest {
		t.Fatal("planning cache ignores runner architecture")
	}
	f.input.Inputs.RunnerDigest = runspec.Hash(strings.Repeat("b", 64))
	third, err := p.Prepare(t.Context(), input, f.input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Input.Inputs.RunnerDigest == third.Input.Inputs.RunnerDigest {
		t.Fatal("planning cache discarded host runner facts")
	}
}

func TestSDKPlanningCancellationDiscardsSuccessfulReplies(t *testing.T) {
	for _, op := range []nodeproto.Op{nodeproto.OpDescribe, nodeproto.OpPlan} {
		t.Run(string(op), func(t *testing.T) {
			p, input, f, processes := sdkPlanningFixture(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			start := p.Start
			p.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, input, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
					value, err := answer(ctx, request)
					if request.Op == op {
						cancel(lease.ErrLeaseLost)
					}
					return value, err
				}
				return process, err
			}
			prepared, err := p.Prepare(ctx, input, f.input)
			if op == nodeproto.OpPlan {
				if err != nil {
					t.Fatal(err)
				}
				var raw json.RawMessage
				raw, err = prepared.Functions.Plan(ctx)
				if len(raw) != 0 {
					t.Fatal("cancelled planning returned declarations")
				}
			}
			if !errors.Is(err, lease.ErrLeaseLost) {
				t.Fatalf("lost cancellation cause: %v", err)
			}
			for _, process := range *processes {
				if process.closed != 1 || process.cleaned != 1 {
					t.Fatal("cancellation leaked its process")
				}
			}
		})
	}
}

func TestSDKPlanningRefusesChangedEngineAndSDK(t *testing.T) {
	for _, mode := range []string{"engine", "sdk"} {
		t.Run(mode, func(t *testing.T) {
			p, input, f, processes := sdkPlanningFixture(t)
			if mode == "engine" {
				p.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
					return engine.Resolved{Identity: engine.Identity{Version: "different"}}, nil
				}
			} else {
				start := p.Start
				p.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
					process, err := start(ctx, input, log)
					proc := process.(*executorProcess)
					proc.answer = func(context.Context, nodeproto.Request) (any, error) {
						return nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: "other"}, Features: []string{}}, nil
					}
					return process, err
				}
			}
			_, err := p.Prepare(t.Context(), input, f.input)
			var failure *apierr.Error
			if !errors.As(err, &failure) {
				t.Fatalf("unstructured refusal: %v", err)
			}
			if mode == "engine" && (failure.Code != apierr.SpecDrift || len(*processes) != 0) {
				t.Fatalf("engine drift started SDK code: %v", err)
			}
			if mode == "sdk" && (failure.Code != apierr.ProtocolMismatch || !strings.Contains(failure.Hint, "matching SDK")) {
				t.Fatalf("missing actionable protocol mismatch: %+v", failure)
			}
			for _, process := range *processes {
				if process.closed != 1 || process.cleaned != 1 {
					t.Fatal("mismatch leaked its process")
				}
			}
		})
	}
}

func TestSDKPlanningReportsBufferedLogLoss(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			p, input, f, _ := sdkPlanningFixture(t)
			var logs []nodeproto.FrameLog
			p.Log = func(_ context.Context, record nodeproto.FrameLog) error { logs = append(logs, record); return nil }
			start := p.Start
			p.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
				process, err := start(ctx, input, log)
				proc := process.(*executorProcess)
				answer := proc.answer
				proc.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpDescribe {
						if err := log(ctx, nodeproto.FrameLog{Message: strings.Repeat("x", 8<<20)}); err != nil {
							return nil, err
						}
						if err := log(ctx, nodeproto.FrameLog{Message: "kept"}); err != nil {
							return nil, err
						}
						if fail {
							return nil, errors.New("describe failed")
						}
					}
					return answer(ctx, request)
				}
				return process, err
			}
			_, err := p.Prepare(t.Context(), input, f.input)
			if (err != nil) != fail {
				t.Fatal(err)
			}
			if fail {
				if len(logs) != 1 || !strings.Contains(logs[0].Message, "2 lines") {
					t.Fatalf("withheld logs not counted: %+v", logs)
				}
			} else if len(logs) != 2 || logs[0].Message != "kept" || !strings.Contains(logs[1].Message, "1 log lines dropped") {
				t.Fatalf("overflow not reported: %+v", logs)
			}
		})
	}
}
