package acceptance

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
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckNativeExecutorSteps exercises step scheduling and diagnostics through the Go SDK process.
func CheckNativeExecutorSteps(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "steps")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/sparkwing-dev/sparkwing/internal/nodehost/testdata/stepsfixture").CombinedOutput(); err != nil {
		t.Fatalf("build SDK fixture: %v: %s", err, out)
	}
	cfg := nodehost.ProtocolSessionConfig{Command: []string{bin}, Environment: []string{"PATH=" + os.Getenv("PATH")}, Revoke: func() error { return nil }}
	runner := nodeproto.RunnerIdentity{Name: "granted", Type: "local", Labels: []string{}}
	var hello nodeproto.HelloResult
	plans := map[bool]nodeproto.PlanDoc{}
	for _, dry := range []bool{false, true} {
		process, err := nodehost.StartProtocolSession(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := process.Close(); err != nil {
				t.Error(err)
			}
		})
		if err := nativeStepCall(ctx, process, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &runner}, &hello); err != nil {
			t.Fatal(err)
		}
		args := map[string]string{}
		if dry {
			args["set"] = "dry"
		}
		var plan nodeproto.PlanDoc
		if err := nativeStepCall(ctx, process, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "steps", Args: args, Run: &nodeproto.RunContext{RunID: "run", Pipeline: "steps", DryRun: dry}}, &plan); err != nil {
			t.Fatal(err)
		}
		plans[dry] = plan
		if err := process.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"order", "parallel", "failfast", "finally-chain", "collect", "continue", "optional", "skips", "skippanic", "panics", "dry"} {
		t.Run(id, func(t *testing.T) {
			var trace []string
			var fakes []*executionSDK
			var deps nodehost.ExecutorDependencies
			input, st, _ := executionHostFixture(t, "success", &trace, &fakes, func(in *nodehost.ExecutorDependencies) { deps = *in })
			records := &nativeStepStore{executionHostStore: st}
			deps.Store = records
			row := &st.snapshot.Nodes[0]
			plan := plans[id == "dry"]
			var job nodeproto.JobSpec
			for _, declared := range plan.Jobs {
				if declared.ID == id {
					job = declared
				}
			}
			if job.ID == "" {
				t.Fatal("fixture job is absent")
			}
			input.NodeID, input.RouteBinding.JobID, row.Ref.NodeID = id, id, id
			input.Engine.Identity.Pin.Version = hello.SDK.Version
			input.Entry = &source.Entry{Engine: input.Engine}
			input.GrantedRunner = runner
			input.Execution.Retry.Value, row.Execution.Retry.Value = 0, 0
			input.Accepted.Plan = plan
			hash, err := expansionHash(plan)
			if err != nil {
				t.Fatal(err)
			}
			input.Accepted.PlanHash = string(hash)
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.PlanHash, spec.Resolved.Engine = hash, input.Engine.Identity
			spec.Request.Request.Pipeline, spec.Resolved.Entrypoint = "steps", "steps"
			spec.Resolved.Settings.Jobs = map[string]runspec.JobExecutionSettings{id: input.Execution}
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
			hash, err = expansionHash(job)
			if err != nil {
				t.Fatal(err)
			}
			input.SpecHash, row.SpecHash = string(hash), string(hash)
			row.Spec, err = json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			deps.ValidatePlan = nil
			deps.Validation = nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: ownerSealSecrets{}}
			deps.Bind = func(_ context.Context, in nodehost.NodeInput) (nodehost.ProcessGrant, error) {
				binding := in.RouteBinding
				binding.Routes = nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "process-fixture"}
				return nodehost.ProcessGrant{Binding: binding, Revoke: func() error { return nil }}, nil
			}
			deps.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return input.Engine, nil
			}
			var child *nativeStepProcess
			var logMu sync.Mutex
			var messages []string
			var diagnostics []nodeproto.FrameLog
			deps.Log = func(_ context.Context, in nodehost.NodeInput, frame nodeproto.FrameLog) error {
				if in.NodeID != id || in.Attempt != input.Attempt+1 || in.Generation != input.Generation {
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
			deps.Start = func(ctx context.Context, _ nodehost.NodeInput, activity nodehost.ExecutorActivity) (nodehost.ExecutorProcess, error) {
				cfg.Activity = activity
				process, err := nodehost.StartProtocolSession(ctx, cfg)
				if process == nil {
					return nil, err
				}
				child = &nativeStepProcess{ExecutorProcess: process}
				return child, err
			}
			deps.PublishOutput = func(_ context.Context, _ nodehost.NodeInput, raw json.RawMessage) (*blob.RecordKey, error) {
				if id == "order" && string(raw) != `"result-from-a"` {
					t.Errorf("dependent lost local output: %s", raw)
				}
				return &blob.RecordKey{}, nil
			}
			host, err := nodehost.NewNodeExecutor(deps)
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
			if child == nil || !child.closed || !child.cleaned {
				t.Fatal("SDK process was not joined")
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
				"optional": {"real work ran"}, "skips": {"ran b"}, "dry": {"dry body", "safe ran"},
			}
			absent := map[string][]string{
				"failfast": {"pending ran"}, "finally-chain": {"pending ran"}, "collect": {"dependent ran"},
				"skips": {"ran a"}, "skippanic": {"guarded ran"}, "dry": {"real body", "plain ran"},
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

type nativeStepStore struct {
	*executionHostStore
	steps []store.StepReport
}

func (s *nativeStepStore) RecordStep(_ context.Context, report store.StepReport) error {
	s.steps = append(s.steps, report)
	return nil
}

type nativeStepProcess struct {
	nodehost.ExecutorProcess
	closed, cleaned bool
}

func (p *nativeStepProcess) Close() error {
	err := p.ExecutorProcess.Close()
	p.closed = true
	return err
}

func (p *nativeStepProcess) Cleanup(ctx context.Context) error {
	err := p.ExecutorProcess.Cleanup(ctx)
	p.cleaned = true
	return err
}

func nativeStepCall(ctx context.Context, process nodehost.ExecutorProcess, request nodeproto.Request, result any) error {
	request.ID = string(request.Op)
	reply, err := process.Request(ctx, request)
	if err != nil {
		return err
	}
	return reply.Decode(result)
}
