package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckHostLifecycleEvents runs the claim host against SQLite transitions with a fake SDK.
func CheckHostLifecycleEvents(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "retry", "before_refused", "output_refused", "result_refused"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			fixture, err := newStoreRecoveryFixture(ctx, filepath.Join(t.TempDir(), "events.db"), false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.s.Close(); err != nil {
					t.Error(err)
				}
			})
			var trace []string
			var processes []*executionSDK
			input, fake, host := executionHostFixture(t, mode, &trace, &processes, func(deps *nodehost.ExecutorDependencies) {
				deps.Store = fixture.tenant
			})
			input.Authority.CapabilityPrefix = fixture.holder.CapabilityPrefix
			installExecutionFixture(t, ctx, fixture, &input, fake)
			row := fake.snapshot.Nodes[0]
			if mode == "result_refused" {
				if _, err := fixture.s.DB().ExecContext(ctx, `CREATE TRIGGER refuse_result BEFORE UPDATE OF outcome ON nodes WHEN NEW.status='done' BEGIN SELECT RAISE(ABORT,'result transaction refused'); END`); err != nil {
					t.Fatal(err)
				}
			}
			host.Store = fixture.tenant
			host.Mask = func(text string) string { return strings.ReplaceAll(text, "private", "***") }
			err = host.Execute(ctx, input)
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
			if err := checkRecoveryEvents(ctx, fixture, want); err != nil {
				t.Fatal(err)
			}
			persisted, err := fixture.tenant.ReadNode(ctx, row.Ref)
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
			before, err := fixture.tenant.ReadStateEvents(ctx, "run", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				result, err := nodehost.FollowReconciliation(ctx, fixture.tenant, store.ReconcileRequest{Ref: row.Ref, Actor: fixture.owner, Policy: store.SourceResume}, nil)
				if err != nil || result.Kind != store.KeepTerminal || result.Claim != nil || len(result.Bearer) != 0 {
					t.Fatalf("adopted terminal result=%+v, error=%v", result, err)
				}
			}
			after, err := fixture.tenant.ReadStateEvents(ctx, "run", 0, 100)
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

func installExecutionFixture(t *testing.T, ctx context.Context, fixture storeRecoveryFixture, input *nodehost.NodeInput, fake *executionHostStore) {
	t.Helper()
	spec := fake.snapshot.Run.Spec
	spec.Request.Request.Team = string(store.DefaultTeam)
	input.Accepted.Spec = marshalEventFixture(t, spec)
	plan := marshalEventFixture(t, input.Accepted.Plan)
	if _, err := fixture.s.DB().ExecContext(ctx, `UPDATE runs SET request_json=?,spec_json=?,plan_json=?,plan_hash=? WHERE id='run'`, marshalEventFixture(t, spec.Request), input.Accepted.Spec, plan, input.Accepted.PlanHash); err != nil {
		t.Fatal(err)
	}
	row := fake.snapshot.Nodes[0]
	if _, err := fixture.s.DB().ExecContext(ctx, `UPDATE nodes SET spec_json=?,spec_hash=?,execution_json=? WHERE run_id='run' AND node_id='job'`, row.Spec, row.SpecHash, marshalEventFixture(t, row.Execution)); err != nil {
		t.Fatal(err)
	}
}

// CheckHostStepPersistence exercises fenced step writes and lifecycle ordering against SQLite.
func CheckHostStepPersistence(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "retry", "before_refused", "step_skip", "step_failed", "step_write_refused", "diagnostic_refused"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			fixture, err := newStoreRecoveryFixture(ctx, filepath.Join(t.TempDir(), "steps.db"), false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.s.Close(); err != nil {
					t.Error(err)
				}
			})
			var trace []string
			var processes []*executionSDK
			var diagnostics []nodeproto.FrameLog
			input, fake, host := executionHostFixture(t, mode, &trace, &processes, func(deps *nodehost.ExecutorDependencies) {
				deps.Store = fixture.tenant
				deps.Log = func(ctx context.Context, in nodehost.NodeInput, log nodeproto.FrameLog) error {
					row, err := fixture.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
					if err != nil || row.Generation != in.Generation || row.Attempt != in.Attempt || row.ClaimedBy != in.Authority.Holder || row.CapabilityPrefix != in.Authority.CapabilityPrefix {
						t.Fatalf("diagnostic differs from committed attempt: %+v, %v", row, err)
					}
					if log.Event == "step_start" || log.Event == "step_end" || log.Event == "step_skipped" {
						var state string
						err := fixture.s.DB().QueryRowContext(ctx, `SELECT status FROM node_steps WHERE team=? AND run_id='run' AND node_id='job' AND attempt=? AND generation=? AND step_id=?`, store.DefaultTeam, in.Attempt, in.Generation, log.Step).Scan(&state)
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
			input.Authority.CapabilityPrefix = fixture.holder.CapabilityPrefix
			installExecutionFixture(t, ctx, fixture, &input, fake)
			host.Store, host.Mask = fixture.tenant, func(text string) string { return text }
			if mode == "step_write_refused" {
				if _, err := fixture.s.DB().ExecContext(ctx, `CREATE TRIGGER refuse_step BEFORE UPDATE OF status ON node_steps WHEN NEW.status='passed' BEGIN SELECT RAISE(ABORT,'step transaction refused'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err = host.Execute(ctx, input)
			if (err != nil) != (mode == "before_refused" || mode == "step_write_refused" || mode == "diagnostic_refused") {
				t.Fatalf("execution=%v trace=%v", err, trace)
			}
			rows, err := fixture.s.DB().QueryContext(ctx, `SELECT attempt,generation,step_id,status,started_at,finished_at FROM node_steps WHERE team=? AND run_id='run' AND node_id='job' ORDER BY attempt,step_id`, store.DefaultTeam)
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
				want[1]["body"] = "running"
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
			persisted, err := fixture.tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
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

type observedClaimRenewal struct {
	nodehost.ClaimStore
	called bool
}

func (s *observedClaimRenewal) RenewNodeClaim(ctx context.Context, ref store.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	s.called = true
	return s.ClaimStore.RenewNodeClaim(ctx, ref, actor)
}

// CheckClaimedHostForeignAwards refuses changed fences before source, renewal or state writes.
func CheckClaimedHostForeignAwards(t *testing.T) {
	t.Helper()
	for _, field := range []string{"holder", "capability", "generation", "attempt"} {
		t.Run(field, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			fixture, err := newStoreRecoveryFixture(ctx, filepath.Join(t.TempDir(), "foreign.db"), false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.s.Close(); err != nil {
					t.Error(err)
				}
			})
			var trace []string
			var processes []*executionSDK
			input, fake, host := executionHostFixture(t, "success", &trace, &processes, func(deps *nodehost.ExecutorDependencies) { deps.Store = fixture.tenant })
			input.Authority.CapabilityPrefix = fixture.holder.CapabilityPrefix
			installExecutionFixture(t, ctx, fixture, &input, fake)
			before, err := fixture.tenant.ReadSnapshot(ctx, "run")
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents, err := fixture.tenant.ReadStateEvents(ctx, "run", 0, 100)
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
			observed := &observedClaimRenewal{ClaimStore: fixture.tenant}
			host.Store, host.Mask = observed, func(text string) string { return text }
			acquired := false
			host.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) {
				acquired = true
				return nil, errors.New("foreign award reached source")
			}
			err = host.Execute(ctx, input)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.LockHeld || observed.called || acquired || len(processes) != 0 {
				t.Fatalf("foreign award reached execution: renewed=%v acquired=%v processes=%d error=%v", observed.called, acquired, len(processes), err)
			}
			after, err := fixture.tenant.ReadSnapshot(ctx, "run")
			if err != nil || !reflect.DeepEqual(before.Run, after.Run) || !reflect.DeepEqual(before.Nodes, after.Nodes) {
				t.Fatalf("foreign award changed persisted state: %v", err)
			}
			afterEvents, err := fixture.tenant.ReadStateEvents(ctx, "run", 0, 100)
			if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
				t.Fatalf("foreign award changed persisted events: %v", err)
			}
		})
	}
}
