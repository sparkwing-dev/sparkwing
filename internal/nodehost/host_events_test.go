package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestHostLifecycleEvents(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "retry", "before_refused", "output_refused", "result_refused"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var fixture *storeHostFixture
			var trace []string
			var processes []*scriptedSDK
			input, _, host := executionHostFixture(t, mode, &trace, &processes, func(deps *ExecutorDependencies) {
				fixture = executionStoreFixture(t, deps)
			})
			input = storedExecutionInput(t, fixture, host)
			row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}}
			if mode == "result_refused" {
				if _, err := teststore.SQL(t, fixture.Store).ExecContext(ctx, `CREATE TRIGGER refuse_result BEFORE UPDATE OF outcome ON nodes WHEN NEW.status='done' BEGIN SELECT RAISE(ABORT,'result transaction refused'); END`); err != nil {
					t.Fatal(err)
				}
			}
			host.Store = fixture.Tenant
			host.Masker = testMasker("private")
			err := host.Execute(ctx, input)
			failed := mode == "before_refused" || mode == "output_refused" || mode == "result_refused"
			if (err != nil) != failed {
				t.Fatalf("trace=%v, execution=%v", trace, err)
			}
			want := []string{"job_started", "job_succeeded"}
			if failed {
				want[1] = "job_failed"
			}
			if mode == "retry" {
				want = []string{"job_started", "job_retrying", "job_retrying", "job_succeeded"}
			}
			if mode == "result_refused" {
				want = []string{"job_started"}
			}
			if err := checkHostEvents(ctx, fixture, want); err != nil {
				t.Fatal(err)
			}
			persisted, err := fixture.Tenant.ReadNode(ctx, row.Ref)
			if err != nil || persisted.Generation != input.Generation || persisted.Attempt != int64(len(processes)) {
				t.Fatalf("execution lost its generation or ordinal: %+v, %v", persisted, err)
			}
			for _, process := range processes {
				if !process.closed || !process.cleaned {
					t.Fatal("SDK process outlived its claim host")
				}
			}
			if mode == "result_refused" {
				if persisted.Status != store.StatusRunning || persisted.Outcome != "" || persisted.FinishedAt != nil {
					t.Fatalf("refused result changed persisted state: %+v", persisted)
				}
				return
			}
			outcome := store.NodeSuccess
			if failed {
				outcome = store.NodeFailed
			}
			if persisted.Status != store.StatusDone || persisted.Outcome != outcome || persisted.ClaimedBy != "" || persisted.CapabilityPrefix != "" {
				t.Fatalf("terminal result retained its claim or lost its verdict: %+v", persisted)
			}
			before, err := fixture.Tenant.ReadStateEvents(ctx, input.Accepted.RunID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				result, err := FollowReconciliation(ctx, fixture.Tenant, store.ReconcileRequest{Ref: row.Ref, Actor: fixture.Actor, Policy: store.SourceResume}, nil)
				if err != nil || result.Kind != store.KeepTerminal || result.Claim != nil || len(result.Bearer) != 0 {
					t.Fatalf("adopted terminal result=%+v, error=%v", result, err)
				}
			}
			after, err := fixture.Tenant.ReadStateEvents(ctx, input.Accepted.RunID, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("adoption changed persisted events: %v", err)
			}
			if mode == "before_refused" {
				for _, entry := range trace {
					if strings.HasPrefix(entry, "body:") {
						t.Fatal("refused hook ran a body")
					}
				}
			}
		})
	}
}

func TestHostStepPersistence(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "retry", "before_refused", "step_skip", "step_failed", "step_write_refused", "diagnostic_refused"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var fixture *storeHostFixture
			var trace []string
			var processes []*scriptedSDK
			var diagnostics []nodeproto.FrameLog
			input, _, host := executionHostFixture(t, mode, &trace, &processes, func(deps *ExecutorDependencies) {
				fixture = executionStoreFixture(t, deps)
				deps.Log = func(ctx context.Context, in NodeInput, log nodeproto.FrameLog) error {
					row, err := fixture.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: in.Accepted.RunID, NodeID: "job"})
					if err != nil || row.Generation != in.Generation || row.Attempt != in.Attempt || row.ClaimedBy != in.Authority.Holder || row.CapabilityPrefix != in.Authority.CapabilityPrefix {
						t.Fatalf("diagnostic differs from committed attempt: %+v, %v", row, err)
					}
					if log.Event == "step_start" || log.Event == "step_end" || log.Event == "step_skipped" {
						var state string
						err := teststore.SQL(t, fixture.Store).QueryRowContext(ctx, `SELECT status FROM node_steps WHERE team=? AND run_id=? AND node_id='job' AND attempt=? AND generation=? AND step_id=?`, store.DefaultTeam, in.Accepted.RunID, in.Attempt, in.Generation, log.Step).Scan(&state)
						if err != nil || log.Event == "step_start" && state != "running" || log.Event == "step_end" && state == "running" || log.Event == "step_skipped" && state != "skipped" {
							t.Fatalf("diagnostic preceded its step commit: %+v, state=%s, error=%v", log, state, err)
						}
					}
					if mode == "diagnostic_refused" && log.Event == "step_end" {
						return errors.New("diagnostic transaction refused")
					}
					diagnostics = append(diagnostics, log)
					return nil
				}
			})
			input = storedExecutionInput(t, fixture, host)
			host.Store, host.Masker = fixture.Tenant, secrets.NewMasker()
			if mode == "step_write_refused" {
				if _, err := teststore.SQL(t, fixture.Store).ExecContext(ctx, `CREATE TRIGGER refuse_step BEFORE UPDATE OF status ON node_steps WHEN NEW.status='passed' BEGIN SELECT RAISE(ABORT,'step transaction refused'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err := host.Execute(ctx, input)
			if (err != nil) != (mode == "before_refused" || mode == "step_write_refused" || mode == "diagnostic_refused") {
				t.Fatalf("execution=%v trace=%v", err, trace)
			}
			rows, err := teststore.SQL(t, fixture.Store).QueryContext(ctx, `SELECT attempt,generation,step_id,status,started_at,finished_at FROM node_steps WHERE team=? AND run_id=? AND node_id='job' ORDER BY attempt,step_id`, store.DefaultTeam, input.Accepted.RunID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			states := map[int64]map[string]string{}
			for rows.Next() {
				var attempt, generation, started int64
				var id, state string
				var finished *int64
				if err := rows.Scan(&attempt, &generation, &id, &state, &started, &finished); err != nil {
					t.Fatal(err)
				}
				if generation != input.Generation || started <= 0 || state != "running" && (finished == nil || *finished < started) {
					t.Fatal("step lost its generation or timestamps")
				}
				if states[attempt] == nil {
					states[attempt] = map[string]string{}
				}
				states[attempt][id] = state
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			want := map[int64]map[string]string{1: {"body": "passed"}}
			switch mode {
			case "retry":
				want = map[int64]map[string]string{1: {"body": "failed"}, 2: {"body": "failed"}, 3: {"body": "passed"}}
			case "before_refused":
				want = map[int64]map[string]string{}
			case "step_skip":
				want[1]["body"] = "skipped"
			case "step_failed":
				want[1] = map[string]string{"body": "failed", "blocked": "cancelled", "cleanup": "passed"}
			case "step_write_refused":
				want[1]["body"] = "failed"
			}
			if !reflect.DeepEqual(states, want) {
				t.Fatalf("persisted step states=%v, want=%v", states, want)
			}
			var events []string
			for _, log := range diagnostics {
				events = append(events, log.Event+":"+log.Step)
			}
			wantEvents := map[string][]string{
				"success": {"step_start:body", "step_end:body"},
				"retry": {
					"step_start:body", "step_end:body", "work_fail_fast:", "retry:",
					"step_start:body", "step_end:body", "work_fail_fast:", "retry:", "step_start:body", "step_end:body",
				},
				"step_skip":          {"step_start:body", "step_skipped:body"},
				"step_failed":        {"step_start:body", "step_end:body", "step_end:blocked", "step_start:cleanup", "step_end:cleanup", "work_fail_fast:"},
				"step_write_refused": {"step_start:body"}, "diagnostic_refused": {"step_start:body"},
			}
			if !reflect.DeepEqual(events, wantEvents[mode]) {
				t.Fatalf("diagnostics=%v, want=%v", events, wantEvents[mode])
			}
			attempts := 1
			if mode == "retry" {
				attempts = 3
			}
			if len(processes) != attempts {
				t.Fatalf("processes=%d, want=%d", len(processes), attempts)
			}
			for _, process := range processes {
				if !process.closed || !process.cleaned {
					t.Fatal("diagnostic failure leaked a process")
				}
			}
			persisted, err := fixture.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: "job"})
			outcome := store.NodeSuccess
			if mode == "before_refused" || mode == "step_failed" || mode == "step_write_refused" || mode == "diagnostic_refused" {
				outcome = store.NodeFailed
			}
			if err != nil || persisted.Outcome != outcome {
				t.Fatalf("job hid its step result: %+v error=%v", persisted, err)
			}
		})
	}
}

func marshalEventFixture(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestClaimedHostForeignAwards(t *testing.T) {
	t.Helper()
	for _, field := range []string{"holder", "capability", "generation", "attempt"} {
		t.Run(field, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var fixture *storeHostFixture
			var trace []string
			var processes []*scriptedSDK
			input, _, host := executionHostFixture(t, "success", &trace, &processes, func(deps *ExecutorDependencies) { fixture = executionStoreFixture(t, deps) })
			input = storedExecutionInput(t, fixture, host)
			before, err := fixture.Tenant.ReadSnapshot(ctx, input.Accepted.RunID)
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents, err := fixture.Tenant.ReadStateEvents(ctx, input.Accepted.RunID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "holder":
				input.Authority.Holder += "-other"
			case "capability":
				input.Authority.CapabilityPrefix += "-other"
			case "generation":
				input.Generation++
			case "attempt":
				input.Attempt++
			}
			observed := false
			observer := &scriptedHostStore{readSnapshot: fixture.Tenant.ReadSnapshot, readNode: fixture.Tenant.ReadNode, transitionNode: fixture.Tenant.TransitionNode, recordStep: fixture.Tenant.RecordStep, renewNodeClaim: func(ctx context.Context, ref controllerwire.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
				observed = true
				return fixture.Tenant.RenewNodeClaim(ctx, ref, actor)
			}}
			host.Store, host.Masker = observer, secrets.NewMasker()
			acquired := false
			host.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) {
				acquired = true
				return nil, errors.New("foreign award reached source")
			}
			err = host.Execute(ctx, input)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.LockHeld || observed || acquired || len(processes) != 0 {
				t.Fatalf("foreign award reached execution: renewed=%v acquired=%v processes=%d error=%v", observed, acquired, len(processes), err)
			}
			after, err := fixture.Tenant.ReadSnapshot(ctx, input.Accepted.RunID)
			if err != nil || !reflect.DeepEqual(before.Run, after.Run) || !reflect.DeepEqual(before.Nodes, after.Nodes) {
				t.Fatalf("foreign award changed persisted state: %v", err)
			}
			afterEvents, err := fixture.Tenant.ReadStateEvents(ctx, input.Accepted.RunID, 0, 100)
			if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
				t.Fatalf("foreign award changed persisted events: %v", err)
			}
		})
	}
}

func executionStoreFixture(t *testing.T, deps *ExecutorDependencies) *storeHostFixture {
	t.Helper()
	fake := deps.Store.(*executionHostFixtureState)
	spec := fake.snapshot.Run.Spec
	request := spec.Request.Request
	request.Version, request.Team = 1, string(store.DefaultTeam)
	request.Git.Repository.Key = "gh:1"
	request.Mode, request.Origin, request.Dispatch = runspec.IntakeExecute, runspec.OriginTrigger, runspec.DispatchHost
	request.Options = spec.Resolved.Settings.Options
	request.Options.JobDefaults.Retry = fake.snapshot.Nodes[0].Execution.Retry
	request.Options.JobDefaults.Retry.Origin.Layer = runspec.LayerFlag
	request.Options.AllowRisk.Origin.Layer = runspec.LayerFlag
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(fake.snapshot.Run.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	fixture := newStoreHostFixtureForRequest(t, openStore(t), plan, request)
	deps.Store = fixture.Tenant
	deps.CanonicalHash = runspec.CanonicalHash
	deps.ValidatePlan = nodeproto.ValidatePlan
	deps.Validation = nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()}
	deps.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
		return engine.Resolved{Identity: fixtureEngine(t, fixture.Accepted)}, nil
	}
	return fixture
}

func fixtureEngine(t *testing.T, accepted AcceptedRun) engine.Identity {
	t.Helper()
	var spec runspec.Spec
	if err := json.Unmarshal(accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	return spec.Resolved.Engine
}

func storedExecutionInput(t *testing.T, fixture *storeHostFixture, host *ClaimedNodeHost) NodeInput {
	t.Helper()
	ctx := t.Context()
	ref := controllerwire.NodeRef{RunID: fixture.Accepted.RunID, NodeID: "job"}
	if _, err := fixture.Tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: fixture.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		t.Fatal(err)
	}
	awarded, err := fixture.Tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: fixture.Actor, Policy: store.SourceNone})
	if err != nil || awarded.Kind != store.Execute {
		t.Fatalf("claim=%+v error=%v", awarded, err)
	}
	selected := engine.Resolved{Identity: fixtureEngine(t, fixture.Accepted)}
	input, err := NewNodeInput(fixture.Accepted, awarded.Node, selected, fixture.Input.GrantedRunner)
	if err != nil {
		t.Fatal(err)
	}
	input.RouteBinding.Routes.Bearer = "parent-authority"
	host.Acquire = func(_ context.Context, spec source.Spec, _ source.Options) (*source.Entry, error) {
		return &source.Entry{Engine: selected, SHA: spec.SHA, RepoKey: spec.RepoKey}, nil
	}
	return input
}

func checkHostEvents(ctx context.Context, fixture *storeHostFixture, want []string) error {
	events, err := fixture.Tenant.ReadStateEvents(ctx, fixture.Accepted.RunID, 0, 100)
	if err != nil {
		return err
	}
	var kinds []string
	for _, event := range events {
		if event.NodeID != "job" {
			continue
		}
		if event.StateSeq != int64(len(kinds)+1) {
			return fmt.Errorf("job event sequence=%d at %s", event.StateSeq, event.Kind)
		}
		kinds = append(kinds, event.Kind)
	}
	want = append([]string{"job_ready", "job_claimed"}, want...)
	if !reflect.DeepEqual(kinds, want) {
		return fmt.Errorf("job events=%v, want=%v", kinds, want)
	}
	return nil
}
