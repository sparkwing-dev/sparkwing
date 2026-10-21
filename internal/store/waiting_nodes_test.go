package store

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestWaitingNodesReadSealedDemandAndPageReadyWork(t *testing.T) {
	s, _ := unitClaimFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET allowed_pools=?`, []byte(`["prod"]`))
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["gpu","pool=prod"]},"resources":{"set":true,"value":{"cores":{"set":true,"value":4},"memory_bytes":{"set":true,"value":1024}}}}`))
	for _, id := range []string{"next", "last"} {
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,'pending',ready_at,spec_json,execution_json FROM nodes WHERE node_id='job'`, id)
	}
	first, err := s.AsOperator().ListWaitingNodes(t.Context(), s.now(), [2]string{}, 2)
	if err != nil || len(first) != 2 || first[0].NodeID != "job" || first[1].NodeID != "last" {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	for _, row := range first {
		if row.Team != "a" || row.Repo.URL != "https://github.com/acme/repo" || !slices.Equal(row.Selector, []string{"gpu", "pool=prod"}) || !slices.Equal(row.AllowedPools, []string{"prod"}) || row.Request.Cores != 4 || row.Request.MemoryBytes != 1024 {
			t.Fatalf("sealed demand: %+v", row)
		}
	}
	last, err := s.AsOperator().ListWaitingNodes(t.Context(), s.now(), [2]string{first[1].RunID, first[1].NodeID}, 2)
	if err != nil || len(last) != 1 || last[0].NodeID != "next" {
		t.Fatalf("last page: %+v, %v", last, err)
	}
}

func TestWaitingNodesRetainTheRunsRunnerSelection(t *testing.T) {
	for _, selected := range []runspec.RunnerSelection{{Kind: runspec.RunnerName, Value: "desktop"}, {Kind: runspec.RunnerLabel, Value: "gpu"}} {
		t.Run(string(selected.Kind), func(t *testing.T) {
			s, _ := unitClaimFixture(t)
			var body []byte
			if err := s.db.QueryRow(`SELECT request_json FROM runs`).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var sealed runspec.SealedRequest
			if err := json.Unmarshal(body, &sealed); err != nil {
				t.Fatal(err)
			}
			sealed.Request.Options.Runner.Optional = runspec.Optional[runspec.RunnerSelection]{Set: true, Value: selected}
			body, err := json.Marshal(sealed)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,repo_display=''`, body)
			waiting, err := s.AsOperator().ListWaitingNodes(t.Context(), s.now(), [2]string{}, 10)
			want := selected.Value
			if selected.Kind == runspec.RunnerName {
				want = "name=" + want
			}
			if err != nil || len(waiting) != 1 || !slices.Equal(waiting[0].Selector, []string{want}) || waiting[0].Repo.URL != "https://github.com/acme/repo" {
				t.Fatalf("run selection: %+v, %v", waiting, err)
			}
		})
	}
}

func TestNodeAttentionUpdatesOnlyWaitingJobsInTheNamedTeam(t *testing.T) {
	for _, update := range []string{
		`UPDATE nodes SET ready_at=NULL`,
		`UPDATE nodes SET claimed_by='runner',capability_prefix='claim',lease_expires_at=2,generation=1`,
		`UPDATE nodes SET status='done',outcome='success',finished_at=1`,
		`UPDATE nodes SET cancel_requested_at=1`,
		`UPDATE runs SET cancel_requested_at=1`,
	} {
		t.Run(update, func(t *testing.T) {
			s, _ := unitClaimFixture(t)
			if err := s.AsOperator().SetNodeAttention(t.Context(), []NodeAttention{{Team: "b", RunID: "run", NodeID: "job", Reason: "foreign"}}); err != nil {
				t.Fatal(err)
			}
			waiting, err := s.AsOperator().ListWaitingNodes(t.Context(), s.now(), [2]string{}, 10)
			if err != nil || len(waiting) != 1 || waiting[0].Attention != "" {
				t.Fatalf("foreign attention: %+v, %v", waiting, err)
			}
			if err := s.AsOperator().SetNodeAttention(t.Context(), []NodeAttention{{Team: "a", RunID: "run", NodeID: "job", Reason: "waiting"}}); err != nil {
				t.Fatal(err)
			}
			waiting, err = s.AsOperator().ListWaitingNodes(t.Context(), s.now(), [2]string{}, 10)
			if err != nil || len(waiting) != 1 || waiting[0].Attention != "waiting" {
				t.Fatalf("waiting attention: %+v, %v", waiting, err)
			}
			rewriteSchemaExec(t, s.db, update)
			if err := s.AsOperator().SetNodeAttention(t.Context(), []NodeAttention{{Team: "a", RunID: "run", NodeID: "job", Reason: "stale"}}); err != nil {
				t.Fatal(err)
			}
			var reason string
			if err := s.db.QueryRow(`SELECT attention_reason FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&reason); err != nil || reason != "waiting" {
				t.Fatalf("stale attention: %q, %v", reason, err)
			}
		})
	}
}

func TestWaitingNodesExcludeWithdrawnAndUnclaimableWork(t *testing.T) {
	for _, update := range []string{
		`UPDATE nodes SET ready_at=NULL`,
		`UPDATE nodes SET ready_at=2`,
		`UPDATE nodes SET claimed_by='runner',capability_prefix='claim',lease_expires_at=2,generation=1`,
		`UPDATE nodes SET cancel_requested_at=1`,
		`UPDATE nodes SET status='done',outcome='success',finished_at=1`,
		`UPDATE runs SET status='success',finished_at=1`,
		`UPDATE runs SET cancel_requested_at=1`,
		`UPDATE runs SET intake_mode='publish'`,
		`UPDATE runs SET dispatch='controller'`,
	} {
		t.Run(update, func(t *testing.T) {
			s, _ := unitClaimFixture(t)
			rewriteSchemaExec(t, s.db, update)
			waiting, err := s.AsOperator().ListWaitingNodes(t.Context(), time.Unix(0, 2), [2]string{}, 10)
			if err != nil || len(waiting) != 0 {
				t.Fatalf("ineligible waiting work: %+v, %v", waiting, err)
			}
		})
	}
}

func TestWaitingNodesRejectCorruptDemand(t *testing.T) {
	for _, body := range []string{`{`, `{"requires":{"value":true}}`, `{"resources":{"value":{"cores":{"value":-1}}}}`} {
		t.Run(body, func(t *testing.T) {
			s, _ := unitClaimFixture(t)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(body))
			if waiting, err := s.AsOperator().ListWaitingNodes(t.Context(), s.now(), [2]string{}, 10); err == nil || len(waiting) != 0 {
				t.Fatalf("corrupt demand: %+v, %v", waiting, err)
			}
		})
	}
}
