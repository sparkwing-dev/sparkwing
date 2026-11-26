package store_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestReaperSelectsOnlyDueUnresolvedApprovalGates(t *testing.T) {
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		st := target.Open(t)
		now := time.Now()
		exec := func(query string, args ...any) {
			t.Helper()
			if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st, query), args...); err != nil {
				t.Fatal(err)
			}
		}
		gate := func(team store.Team, node, status string, requested time.Time, timeoutMS int64, resolved bool) {
			t.Helper()
			exec(`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES (?,?,?,?,?,?)`, team, "run", node, status, []byte(`{"id":"`+node+`"}`), []byte(`{}`))
			resolution, resolvedAt := "", any(nil)
			if resolved {
				resolution, resolvedAt = "approved", now.UnixNano()
			}
			exec(`INSERT INTO approvals(team,run_id,node_id,requested_at,timeout_ms,resolved_at,resolution) VALUES (?,?,?,?,?,?,?)`, team, "run", node, requested.UnixNano(), timeoutMS, resolvedAt, resolution)
		}
		for i, team := range []store.Team{"acme", "beta"} {
			tenantFor(t, st, team)
			exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days)
VALUES (?,'run',?,'ci','running',?,'request',1,'gh:1','host','execute','account',?,'r30',30)`, team, i+1, []byte(`{}`), now.Add(-time.Hour).UnixNano())
			gate(team, "due", "approval_pending", now.Add(-time.Hour), 1000, false)
		}
		gate("acme", "early", "approval_pending", now, time.Hour.Milliseconds(), false)
		gate("acme", "indefinite", "approval_pending", now.Add(-time.Hour), 0, false)
		gate("acme", "resolved", "approval_pending", now.Add(-time.Hour), 1000, true)
		gate("acme", "moved_on", "pending", now.Add(-time.Hour), 1000, false)

		got, err := st.AsOperator().ExpiredUnitsForTest(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"acme/run/due/node", "beta/run/due/node"}
		if !slices.Equal(got, want) {
			t.Fatalf("expired units %q, want %q", got, want)
		}
	})
}
