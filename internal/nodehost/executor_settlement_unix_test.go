//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNodeExecutorDrivesNativeSDKSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "steps")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "./testdata/stepsfixture").CombinedOutput(); err != nil {
		t.Fatalf("build SDK fixture: %v: %s", err, out)
	}
	cfg := ProtocolSessionConfig{Command: []string{bin}, Environment: []string{"PATH=" + os.Getenv("PATH")}, Revoke: func() error { return nil }}
	process, err := StartProtocolSession(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	session := &executorSession{process: process}
	runner := nodeproto.RunnerIdentity{Name: "granted", Type: "local", Labels: []string{}}
	var hello nodeproto.HelloResult
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &runner}, &hello); err != nil {
		t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "steps", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "run", Pipeline: "steps"}}, &plan); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"order", "parallel", "failfast", "finally-chain", "collect", "continue", "optional", "skips", "skippanic", "panics"} {
		t.Run(id, func(t *testing.T) {
			input, st, deps, _ := executorFixture(t)
			var job nodeproto.JobSpec
			for _, declared := range plan.Jobs {
				if declared.ID == id {
					job = declared
				}
			}
			if job.ID == "" {
				t.Fatal("fixture job is absent")
			}
			input.NodeID, input.RouteBinding.JobID, st.row.Ref.NodeID = id, id, id
			input.Engine.Identity.Pin.Version = hello.SDK.Version
			input.Entry.Engine = input.Engine
			input.GrantedRunner = runner
			input.Accepted.Plan = plan
			hash, err := executorHash(plan)
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
			input.Accepted.Spec, err = json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			hash, err = executorHash(job)
			if err != nil {
				t.Fatal(err)
			}
			input.SpecHash, st.row.SpecHash = string(hash), string(hash)
			st.row.Spec, err = json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			deps.ValidatePlan = nil
			deps.Validation = nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}
			deps.Bind = func(_ context.Context, in NodeInput) (nodeproto.ProcessBinding, error) {
				binding := in.RouteBinding
				binding.Routes = nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "process-fixture"}
				return binding, nil
			}
			deps.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				return input.Engine, nil
			}
			var child *ProtocolSession
			cleanupRan := false
			cfg.Log = func(_ context.Context, frame nodeproto.FrameLog) error {
				if frame.Message == "cleanup ran" {
					cleanupRan = true
				}
				return nil
			}
			deps.Start = func(ctx context.Context, _ NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				cfg.Activity = activity
				var err error
				child, err = StartProtocolSession(ctx, cfg)
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
			final := st.changes[len(st.changes)-1]
			if final.To != want || err != nil && id != "skippanic" {
				t.Fatalf("outcome=%s, want=%s, error=%v, result=%+v", final.To, want, err, final.Extra.Result)
			}
			if final.Extra.Result != nil && strings.Contains(final.Extra.Result.MaskedError, "protocol") {
				t.Fatalf("SDK rejected host state: %+v", final.Extra.Result)
			}
			if child == nil || child.cmd.ProcessState == nil {
				t.Fatal("SDK process was not joined")
			}
			if id == "finally-chain" && !cleanupRan {
				t.Fatal("cleanup ran before its transitive prerequisites settled")
			}
		})
	}
}
