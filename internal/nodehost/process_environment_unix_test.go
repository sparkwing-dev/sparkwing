//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeSDKStartsUseEnvironmentPolicyAndRevokeFailures(t *testing.T) {
	for _, placement := range []struct {
		name  string
		kind  procenv.Kind
		retry bool
	}{{"plan", procenv.Plan, false}, {"attempt", procenv.Attempt, false}, {"eval", procenv.Eval, false}, {"retry_attempt", procenv.Attempt, true}, {"retry_eval", procenv.Eval, true}} {
		kind := placement.kind
		for _, refuse := range []bool{false, true} {
			t.Run(placement.name+map[bool]string{false: "/success", true: "/refused"}[refuse], func(t *testing.T) {
				input, st, _, _ := executorFixture(t)
				if placement.retry {
					var spec runspec.Spec
					if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
						t.Fatal(err)
					}
					spec.Request.RunID = "source-run"
					input.Accepted.Spec, _ = json.Marshal(spec)
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				input.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "environment"}
				policy := sdkTestEnvironment(st)
				refused := errors.New("environment refused")
				built := false
				want := []string{"CAPTURED=value", "GORACE=atexit_sleep_ms=0"}
				policy.Build = func(gotKind procenv.Kind, _ procenv.Base, runtime procenv.Runtime) (procenv.Env, error) {
					built = true
					if gotKind != kind || runtime.RunID != "run" || (kind != procenv.Plan && runtime.JobID != input.NodeID) {
						t.Fatalf("wrong environment policy: kind=%v runtime=%+v", gotKind, runtime)
					}
					if refuse {
						return procenv.Env{}, refused
					}
					return procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: want}, procenv.Runtime{})
				}
				revoked := 0
				cfg := ProtocolSessionConfig{Environments: policy, Environment: testProcessEnv(t, "UNBUILT=unsafe"), ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil }, Revoke: func() error { revoked++; return nil }}
				var process ExecutorProcess
				switch kind {
				case procenv.Plan:
					process, err = cfg.StartPlanning(t.Context(), PlanningProcessInput{Entry: input.Entry, Runner: input.GrantedRunner, Run: nodeproto.RunContext{RunID: "run", Pipeline: "ci"}}, func(context.Context, nodeproto.FrameLog) error { return nil })
				case procenv.Attempt:
					process, err = cfg.Start(t.Context(), input, ExecutorActivity{})
				case procenv.Eval:
					process, err = cfg.StartEvaluation(t.Context(), EvaluationInput{Entry: input.Entry, Accepted: input.Accepted, NodeID: input.NodeID, Generation: int64(input.Generation), SpecHash: input.SpecHash, GrantedRunner: input.GrantedRunner})
				}
				if !built {
					t.Fatal("native startup bypassed environment policy")
				}
				if refuse {
					if process != nil || !errors.Is(err, refused) || revoked != 1 {
						t.Fatalf("process=%v error=%v revocations=%d", process, err, revoked)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = process.Close() })
				session := process.(*ProtocolSession)
				protocolTestHello(t, session)
				reply, err := session.Request(t.Context(), nodeproto.Request{ID: "env", Op: nodeproto.OpDescribe})
				if err != nil {
					t.Fatal(err)
				}
				var reported, actual []string
				// safety: the fixture binary's testleak initializer adds its marker and sandbox home after exec.
				err = json.Unmarshal(reply.Result, &reported)
				for _, entry := range reported {
					name, _, _ := strings.Cut(entry, "=")
					if !slices.Contains([]string{"SPARKWING_TESTLEAK_HOST", "HOME", "SPARKWING_HOME", "XDG_CONFIG_HOME"}, name) {
						actual = append(actual, entry)
					}
				}
				if err != nil || !reflect.DeepEqual(actual, want) || !slices.Contains(reported, "SPARKWING_TESTLEAK_HOST="+exe) {
					t.Fatalf("environment=%v error=%v", actual, err)
				}
				if err := process.Close(); err != nil || revoked != 1 || session.cmd.ProcessState == nil {
					t.Fatalf("close=%v revocations=%d", err, revoked)
				}
			})
		}
	}
}

func TestNativeSDKStartsLoadCapturedValuesBeforeSharedEnvironmentPolicy(t *testing.T) {
	for _, placement := range []string{"plan", "attempt", "eval", "retry_attempt", "retry_eval"} {
		t.Run(placement, func(t *testing.T) {
			input, st, _, _ := executorFixture(t)
			snapshots, err := NewFileEnvironmentSnapshots(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			seal, err := snapshots.Capture(t.Context(), []string{"GORACE=atexit_sleep_ms=0", "CAPTURED=original", "EMPTY=", "API_TOKEN=private-token", "SPARKWING_RUN_ID=spoofed"})
			if err != nil {
				t.Fatal(err)
			}
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.Request.Request.Env = seal
			if strings.HasPrefix(placement, "retry_") {
				spec.Request.RunID = "source-run"
			}
			input.Accepted.Spec, _ = json.Marshal(spec)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			input.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "environment"}
			revocations := 0
			cfg := ProtocolSessionConfig{
				Environments: SDKEnvironment{Snapshots: snapshots, Store: st, Base: procenv.Base{Local: true, Captured: []string{"CAPTURED=changed"}, Ambient: []string{"AMBIENT=daemon"}}},
				ParseFrame:   protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil },
				Revoke: func() error { revocations++; return nil },
			}
			var process ExecutorProcess
			kind := strings.TrimPrefix(placement, "retry_")
			switch kind {
			case "plan":
				process, err = cfg.StartPlanning(t.Context(), PlanningProcessInput{Entry: input.Entry, Environment: seal, Run: nodeproto.RunContext{RunID: input.Accepted.RunID, Pipeline: "build"}}, func(context.Context, nodeproto.FrameLog) error { return nil })
			case "attempt":
				process, err = cfg.Start(t.Context(), input, ExecutorActivity{})
			case "eval":
				process, err = cfg.StartEvaluation(t.Context(), EvaluationInput{Entry: input.Entry, Accepted: input.Accepted, NodeID: input.NodeID, Generation: int64(input.Generation), SpecHash: input.SpecHash})
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Close() })
			protocolTestHello(t, process.(*ProtocolSession))
			reply, err := process.Request(t.Context(), nodeproto.Request{ID: "environment", Op: nodeproto.OpDescribe})
			if err != nil {
				t.Fatal(err)
			}
			var environment []string
			if err := json.Unmarshal(reply.Result, &environment); err != nil {
				t.Fatal(err)
			}
			values := map[string]string{}
			for _, entry := range environment {
				name, value, _ := strings.Cut(entry, "=")
				values[name] = value
			}
			empty, present := values["EMPTY"]
			if values["CAPTURED"] != "original" || !present || empty != "" || values["SPARKWING_RUN_ID"] != input.Accepted.RunID {
				t.Fatalf("SDK lost the captured base or sealed identity: %v", environment)
			}
			if _, ok := values["API_TOKEN"]; ok || values["AMBIENT"] != "" || kind != "plan" && values["SPARKWING_JOB_ID"] != input.NodeID {
				t.Fatal("SDK inherited credentials, daemon input or the wrong job identity")
			}
			if err := process.Close(); err != nil || revocations != 1 {
				t.Fatalf("close=%v revocations=%d", err, revocations)
			}
		})
	}
}

func sdkStartupJob(value string) nodeproto.JobSpec {
	return nodeproto.JobSpec{ID: "build", Env: map[string]nodeproto.EnvValue{"NODE_OPTIONS": {Value: value}}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
}

func TestNativeSDKStartupChangeRefusesDriftBeforeBody(t *testing.T) {
	for _, value := range []string{"stable", "change"} {
		t.Run(value, func(t *testing.T) {
			input, st, deps, _ := executorFixture(t)
			job := sdkStartupJob(value)
			input.Accepted.Plan.Jobs = []nodeproto.JobSpec{job}
			planHash, _ := runspec.CanonicalHash(input.Accepted.Plan)
			input.Accepted.PlanHash = string(planHash)
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.PlanHash = planHash
			input.Accepted.Spec, _ = json.Marshal(spec)
			st.accepted = &input.Accepted
			jobHash, _ := runspec.CanonicalHash(job)
			st.row.Spec, _ = json.Marshal(job)
			st.row.SpecHash, input.SpecHash = string(jobHash), string(jobHash)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			input.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "startup_" + value}
			policy := sdkTestEnvironment(st)
			policy.Split = procenv.SplitJobEnv
			policy.Build = func(_ procenv.Kind, _ procenv.Base, runtime procenv.Runtime) (procenv.Env, error) {
				env := []string{"GORACE=atexit_sleep_ms=0"}
				for _, assignment := range runtime.Startup {
					env = append(env, assignment.Name+"="+assignment.Value)
				}
				return procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: env}, procenv.Runtime{})
			}
			body, revoked, cleaned := false, false, false
			cfg := ProtocolSessionConfig{
				Environments: policy, ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil },
				Revoke: func() error { revoked = true; return nil }, RunCleanups: func(context.Context, CleanupScope) error { cleaned = true; return nil },
				Log: func(_ context.Context, log nodeproto.FrameLog) error {
					body = body || log.Message == "body entered"
					return nil
				},
			}
			deps.Start = cfg.Start
			deps.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return input.Engine, nil
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(t.Context(), input)
			wantBody := value == "stable"
			if body != wantBody || (err == nil) != wantBody || !revoked || !cleaned {
				t.Fatalf("body=%t revoked=%t cleaned=%t error=%v", body, revoked, cleaned, err)
			}
			last := st.changes[len(st.changes)-1]
			if last.Cause != store.CauseReport || wantBody && last.To != store.NodeSuccess || !wantBody && (last.To != store.NodeFailed || !strings.Contains(last.Extra.Result.Reason, "plan_drift")) {
				t.Fatalf("transition=%+v", last)
			}
		})
	}
}

func TestNativeSDKCleanupGetsCompleteAttemptEnvironment(t *testing.T) {
	for _, kind := range []procenv.Kind{procenv.Attempt, procenv.Eval} {
		t.Run(map[procenv.Kind]string{procenv.Attempt: "attempt", procenv.Eval: "eval"}[kind], func(t *testing.T) {
			input, st, _, _ := executorFixture(t)
			input.Entry.Directory = t.TempDir()
			job := nodeproto.JobSpec{ID: input.NodeID, Env: map[string]nodeproto.EnvValue{
				"GOMAXPROCS": {Value: "2"}, "NODE_OPTIONS": {Value: "--no-warnings"},
				"DOCKER_HOST": {Value: "tcp://declared:2375"}, "BODY": {Value: "after-plan"},
			}}
			st.row.Spec, _ = json.Marshal(job)
			captured := []string{"GORACE=atexit_sleep_ms=0", "DOCKER_HOST=tcp://captured:2375", "API_TOKEN=private-value"}
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.Request.Request.Env, _ = runspec.EnvironmentDigest(map[string]string{"GORACE": "atexit_sleep_ms=0", "DOCKER_HOST": "tcp://captured:2375", "API_TOKEN": "private-value"}, strings.Repeat("5", 64))
			input.Accepted.Spec, _ = json.Marshal(spec)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			input.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "environment"}
			cleanups := &ProcessCleanups{}
			if _, err := cleanups.Register([]string{"/bin/sh", "-c", `printf '%s' "$DOCKER_HOST" > cleanup-env`}, ""); err != nil {
				t.Fatal(err)
			}
			revoked, cleaned := false, false
			cfg := ProtocolSessionConfig{
				Environments: SDKEnvironment{Base: procenv.Base{Local: true, Captured: captured}, Store: st},
				ParseFrame:   protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil },
				Revoke: func() error { revoked = true; return nil },
				RunCleanups: func(ctx context.Context, scope CleanupScope) error {
					cleaned = true
					if !revoked || scope.Dir != input.Entry.Directory {
						t.Fatal("cleanup ran before revocation or in another directory")
					}
					for _, entry := range []string{"GOMAXPROCS=2", "NODE_OPTIONS=--no-warnings", "BODY=after-plan", "DOCKER_HOST=tcp://declared:2375", "SPARKWING_JOB_ID=build"} {
						if !slices.Contains(slices.Collect(scope.Env.Values()), entry) {
							t.Fatalf("cleanup omitted %s: %v", entry, scope.Env)
						}
					}
					if strings.Contains(strings.Join(slices.Collect(scope.Env.Values()), "\n"), "private-value") || slices.Contains(slices.Collect(scope.Env.Values()), "DOCKER_HOST=tcp://captured:2375") {
						t.Fatal("cleanup inherited a credential or lost the body override")
					}
					return cleanups.Run(ctx, scope)
				},
			}
			var process ExecutorProcess
			if kind == procenv.Attempt {
				process, err = cfg.Start(t.Context(), input, ExecutorActivity{})
			} else {
				process, err = cfg.StartEvaluation(t.Context(), EvaluationInput{Entry: input.Entry, Accepted: input.Accepted, NodeID: input.NodeID, Generation: int64(input.Generation), SpecHash: input.SpecHash})
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Close() })
			session := process.(*ProtocolSession)
			protocolTestHello(t, session)
			reply, err := session.Request(t.Context(), nodeproto.Request{ID: "environment", Op: nodeproto.OpDescribe})
			if err != nil {
				t.Fatal(err)
			}
			var startup []string
			if err := json.Unmarshal(reply.Result, &startup); err != nil {
				t.Fatal(err)
			}
			for _, entry := range []string{"GOMAXPROCS=2", "NODE_OPTIONS=--no-warnings", "DOCKER_HOST=tcp://captured:2375"} {
				if !slices.Contains(startup, entry) {
					t.Fatalf("native startup omitted %s", entry)
				}
			}
			if slices.Contains(startup, "BODY=after-plan") || strings.Contains(strings.Join(startup, "\n"), "private-value") {
				t.Fatal("body value or credential reached startup")
			}
			if err := process.Close(); err != nil {
				t.Fatal(err)
			}
			if err := process.Cleanup(t.Context()); err != nil || !cleaned {
				t.Fatalf("cleanup=%t error=%v", cleaned, err)
			}
			if raw, err := os.ReadFile(filepath.Join(input.Entry.Directory, "cleanup-env")); err != nil || string(raw) != "tcp://declared:2375" {
				t.Fatalf("cleanup tool environment=%q error=%v", raw, err)
			}
		})
	}
}
