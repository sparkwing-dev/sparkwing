package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeExecutorSteps(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := buildNativeSDKFixture(t, ctx, "stepsfixture")
	cfg := ProtocolSessionConfig{Command: []string{bin}, Environment: []string{"PATH=" + os.Getenv("PATH")}, Revoke: func() error { return nil }}
	runner := nodeproto.RunnerIdentity{Name: "granted", Type: "local", Labels: []string{}}
	var hello nodeproto.HelloResult
	plans := map[bool]nodeproto.PlanDoc{}
	for _, dry := range []bool{false, true} {
		args := map[string]string{}
		if dry {
			args["set"] = "dry"
		}
		var plan nodeproto.PlanDoc
		hello, plan = planNativeSDKFixture(t, ctx, bin, "steps", args, nodeproto.RunContext{RunID: "run", Pipeline: "steps", DryRun: dry}, runner)
		plans[dry] = plan
	}
	for _, id := range []string{"order", "parallel", "failfast", "finally-chain", "collect", "continue", "optional", "skips", "window", "skippanic", "panics", "dry"} {
		t.Run(id, func(t *testing.T) {
			jobID := id
			if id == "window" {
				jobID = "skips"
			}
			var trace []string
			var fakes []*scriptedSDK
			var deps ExecutorDependencies
			input, st, _ := executionHostFixture(t, "success", &trace, &fakes, func(in *ExecutorDependencies) { deps = *in })
			records := st
			records.recordStep = func(_ context.Context, report store.StepReport) error {
				records.steps = append(records.steps, report)
				return nil
			}
			deps.Store = records
			row := &st.snapshot.Nodes[0]
			plan := plans[id == "dry"]
			var job nodeproto.JobSpec
			for _, declared := range plan.Jobs {
				if declared.ID == jobID {
					job = declared
				}
			}
			if job.ID == "" {
				t.Fatal("fixture job is absent")
			}
			input.NodeID, input.RouteBinding.JobID, row.Ref.NodeID = jobID, jobID, jobID
			input.Engine.Identity.Pin.Version = hello.SDK.Version
			input.Entry = &source.Entry{Engine: input.Engine}
			input.GrantedRunner = runner
			input.Execution.Retry.Value, row.Execution.Retry.Value = 0, 0
			input.Accepted.Plan = plan
			hash, err := runspec.CanonicalHash(plan)
			if err != nil {
				t.Fatal(err)
			}
			input.Accepted.PlanHash = string(hash)
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.PlanHash, spec.Resolved.Engine = hash, input.Engine.Identity
			spec.Request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{"PATH": os.Getenv("PATH")}, strings.Repeat("5", 64))
			if err != nil {
				t.Fatal(err)
			}
			spec.Request.Request.Pipeline, spec.Resolved.Entrypoint = "steps", "steps"
			spec.Resolved.Settings.Jobs = map[string]runspec.JobExecutionSettings{jobID: input.Execution}
			if id == "window" {
				spec.Resolved.Settings.Options.StartAt = runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "b"}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag}}
				spec.Request.Request.Options.StartAt = spec.Resolved.Settings.Options.StartAt
			}
			if id == "dry" {
				spec.Resolved.Args = map[string]string{"set": "dry"}
				spec.Resolved.Settings.Options.DryRun.Value = true
			}
			input.Accepted.Spec, err = json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			st.snapshot.Run.Spec, st.snapshot.Run.PlanHash = &spec, input.Accepted.PlanHash
			st.snapshot.Run.Plan, err = json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			hash, err = runspec.CanonicalHash(job)
			if err != nil {
				t.Fatal(err)
			}
			input.SpecHash, row.SpecHash = string(hash), string(hash)
			row.Spec, err = json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}

			deps.Validation = nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: ownerSealSecrets{}}
			revoked := false
			deps.Bind = func(_ context.Context, in NodeInput) (ProcessGrant, error) {
				binding := in.RouteBinding
				binding.Routes = nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "process-fixture"}
				return ProcessGrant{Binding: binding, Revoke: func() error { revoked = true; return nil }}, nil
			}
			deps.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return input.Engine, nil
			}
			var child *scriptedSDK
			var logMu sync.Mutex
			var messages []string
			var diagnostics []nodeproto.FrameLog
			deps.Log = func(_ context.Context, in NodeInput, frame nodeproto.FrameLog) error {
				if in.NodeID != jobID || in.Attempt != input.Attempt+1 || in.Generation != input.Generation {
					t.Fatal("native diagnostic lost its attempt attribution")
				}
				diagnostics = append(diagnostics, frame)
				return nil
			}
			cfg.Log = func(_ context.Context, frame nodeproto.FrameLog) error {
				logMu.Lock()
				defer logMu.Unlock()
				messages = append(messages, frame.Message)
				return nil
			}
			attemptConfig := cfg
			attemptConfig.Revoke = nil
			attemptConfig.Environments = SDKEnvironment{Store: records, Base: procenv.Base{Local: true, Captured: []string{"PATH=" + os.Getenv("PATH")}}}
			input.Entry.Command, input.Entry.Directory = cfg.Command, t.TempDir()
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := attemptConfig.Start(ctx, in, activity)
				if process == nil {
					return nil, err
				}
				child = &scriptedSDK{request: process.Request, onClose: process.Close, onCleanup: process.Cleanup}
				return child, err
			}
			deps.PublishOutput = func(_ context.Context, _ NodeInput, raw json.RawMessage) (*blob.RecordKey, error) {
				if id == "order" && string(raw) != `"result-from-a"` {
					t.Errorf("dependent lost local output: %s", raw)
				}
				return &blob.RecordKey{}, nil
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(ctx, input)
			want := store.NodeSuccess
			if id == "failfast" || id == "finally-chain" || id == "collect" || id == "continue" || id == "skippanic" || id == "panics" {
				want = store.NodeFailed
			}
			final := st.reports[len(st.reports)-1]
			if final.To != want || err != nil && id != "skippanic" {
				t.Fatalf("outcome=%s, want=%s, error=%v, result=%+v", final.To, want, err, final.Extra.Result)
			}
			if final.Extra.Result != nil && strings.Contains(final.Extra.Result.MaskedError, "protocol") {
				t.Fatalf("SDK rejected host state: %+v", final.Extra.Result)
			}
			wantError := map[string]string{"failfast": "rejected", "finally-chain": "rejected", "collect": "rejected", "continue": "rejected", "skippanic": "guard blew up", "panics": "dropped a wrench"}
			if text := wantError[id]; text != "" && (final.Extra.Result == nil || !strings.Contains(final.Extra.Result.MaskedError, text)) {
				t.Fatalf("native failure lost its SDK error: %+v", final.Extra.Result)
			}
			if child == nil || !child.closed || !child.cleaned || !revoked {
				t.Fatal("SDK process was not joined")
			}
			if id == "window" {
				for _, request := range child.requests {
					if request.Op == nodeproto.OpEval || request.Op == nodeproto.OpRunStep && request.Step == "a" {
						t.Fatal("excluded step invoked a predicate or body")
					}
				}
			}
			states := map[string]string{}
			for _, report := range records.steps {
				states[report.StepID] = report.State
			}
			wantStates := map[string]map[string]string{
				"order":         {"a": "passed", "c1": "passed", "c2": "passed", "b": "passed"},
				"parallel":      {"p1": "passed", "p2": "passed", "p3": "passed"},
				"failfast":      {"setup": "passed", "slow": "cancelled", "reject": "failed", "pending": "cancelled", "cleanup": "passed"},
				"finally-chain": {"slow": "cancelled", "reject": "failed", "pending": "cancelled", "cleanup": "passed"},
				"collect":       {"reject": "failed", "independent": "passed", "dependent": "cancelled"},
				"continue":      {"reject": "failed", "dependent": "passed"},
				"optional":      {"best-effort": "failed", "real-work": "passed"},
				"skips":         {"a": "skipped", "b": "passed"},
				"window":        {"a": "skipped", "b": "passed"},
				"skippanic":     {"guarded": "failed"}, "panics": {"oops": "failed"},
				"dry": {"with-dry": "passed", "safe": "passed", "plain": "skipped"},
			}
			if !maps.Equal(states, wantStates[id]) {
				t.Fatalf("native step records=%v, want=%v", states, wantStates[id])
			}
			outcomes := map[string]string{}
			failedAt, firstCancel, failFastCount := -1, -1, 0
			for i, frame := range diagnostics {
				var attrs map[string]any
				if err := json.Unmarshal(frame.Attrs, &attrs); err != nil {
					t.Fatal(err)
				}
				if frame.Event == "step_end" || frame.Event == "step_skipped" {
					outcomes[frame.Message] = fmt.Sprint(attrs["outcome"])
					if frame.Event == "step_end" && attrs["duration_ms"] == nil {
						t.Fatal("native step diagnostic lacks duration")
					}
					if attrs["outcome"] == "failed" && failedAt == -1 {
						failedAt = i
					}
					if attrs["outcome"] == "cancelled" && firstCancel == -1 {
						firstCancel = i
					}
				}
				if frame.Event == "step_skipped" && id == "skips" && attrs["reason"] != nil {
					t.Fatal("SkipIf added a dry-run reason")
				}
				if frame.Event == "step_skipped" && id == "dry" && attrs["reason"] != "no_dry_run_defined" {
					t.Fatal("dry-run diagnostic lost its reason")
				}
				if frame.Event == "work_fail_fast" {
					failFastCount++
					if job.FailurePolicy == "collect_all" {
						t.Fatal("collect-all emitted fail-fast diagnostics")
					}
					if id == "failfast" && (frame.Message != "reject" || attrs["trigger_step"] != "reject" || fmt.Sprint(attrs["cancelled_steps"]) != "[pending slow]" || attrs["cancelled_siblings"] != float64(2) || attrs["cancellation_latency_ms"] == nil) {
						t.Fatalf("fail-fast diagnostic=%+v", frame)
					}
				}
			}
			if (id == "failfast" || id == "finally-chain") && failFastCount != 1 {
				t.Fatalf("fail-fast diagnostics=%d, want=1", failFastCount)
			}
			wantOutcomes := maps.Clone(wantStates[id])
			for step, state := range wantOutcomes {
				if state == "passed" {
					wantOutcomes[step] = "success"
				}
			}
			if !maps.Equal(outcomes, wantOutcomes) || firstCancel >= 0 && (failedAt < 0 || failedAt > firstCancel) {
				t.Fatalf("native diagnostics=%v, want=%v, failure=%d first cancellation=%d", outcomes, wantOutcomes, failedAt, firstCancel)
			}
			present := map[string][]string{
				"order": {"b saw from-a after 2 checks"}, "failfast": {"slow saw its cancel", "cleanup saw env-7"},
				"finally-chain": {"cleanup ran"}, "collect": {"independent finished"}, "continue": {"dependent ran"},
				"optional": {"real work ran"}, "skips": {"ran b"}, "window": {"ran b"}, "dry": {"dry body", "safe ran"},
			}
			absent := map[string][]string{
				"failfast": {"pending ran"}, "finally-chain": {"pending ran"}, "collect": {"dependent ran"},
				"skips": {"ran a"}, "window": {"ran a"}, "skippanic": {"guarded ran"}, "dry": {"real body", "plain ran"},
			}
			text := strings.Join(messages, "\n")
			for _, message := range present[id] {
				if !strings.Contains(text, message) {
					t.Fatalf("native output lacks %q: %s", message, text)
				}
			}
			for _, message := range absent[id] {
				if strings.Contains(text, message) {
					t.Fatalf("native output contains %q: %s", message, text)
				}
			}
		})
	}
}

func buildNativeSDKFixture(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/sparkwing-dev/sparkwing/internal/nodehost/testdata/"+name).CombinedOutput(); err != nil {
		t.Fatalf("build SDK fixture: %v: %s", err, out)
	}
	return bin
}

func planNativeSDKFixture(t *testing.T, ctx context.Context, bin, pipeline string, args map[string]string, run nodeproto.RunContext, runner nodeproto.RunnerIdentity) (nodeproto.HelloResult, nodeproto.PlanDoc) {
	t.Helper()
	process, err := StartProtocolSession(ctx, ProtocolSessionConfig{Command: []string{bin}, Environment: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	}()
	var hello nodeproto.HelloResult
	if err := nativeStepCall(ctx, process, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &runner}, &hello); err != nil {
		t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := nativeStepCall(ctx, process, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: pipeline, Args: args, Run: &run}, &plan); err != nil {
		t.Fatal(err)
	}
	return hello, plan
}

func nativeStepCall(ctx context.Context, process ExecutorProcess, request nodeproto.Request, result any) error {
	request.ID = string(request.Op)
	reply, err := process.Request(ctx, request)
	if err != nil {
		return err
	}
	return reply.Decode(result)
}
