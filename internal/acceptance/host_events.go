package acceptance

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
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

func marshalEventFixture(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
