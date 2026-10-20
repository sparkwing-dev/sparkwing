package teststore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SeedRun inserts reporting facts without admitting executable work.
func SeedRun(s *store.Store, ctx context.Context, team store.Team, r store.Run) error {
	if _, err := s.ForTeam(ctx, team); err != nil {
		return err
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = r.StartedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
	}
	repository := r.DeclaredRepo
	if repository == "" {
		repository = r.RepoURL
	}
	sha := ""
	if runspec.IsCommitID(r.GitSHA) {
		sha = r.GitSHA
	}
	provenance, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "fixture"), r.GitBranch, sha, r.Pipeline)
	if err != nil {
		return err
	}
	request := runspec.SealedRequest{Version: 1, RunID: r.ID, AdmittedAt: created, Provenance: provenance, Request: runspec.Request{
		Version: 1, Team: string(team), Pipeline: r.Pipeline, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost,
		RequestedArgs: r.Args, Actor: runspec.Actor{Display: r.TriggerUser},
		Git:     runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: repository}, Ref: r.GitBranch, SHA: r.GitSHA},
		Source:  runspec.Source{Location: runspec.SourceLocation{RemoteURL: r.RepoURL}},
		Lineage: runspec.Lineage{ParentRunID: r.ParentRunID, RetryOf: r.RetryOf},
	}}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var started, finished, parent any
	if !r.StartedAt.IsZero() {
		started = r.StartedAt.UnixNano()
	}
	if r.FinishedAt != nil {
		finished = r.FinishedAt.UnixNano()
	} else if r.Status == "success" || r.Status == "failed" || r.Status == "cancelled" {
		finished = created.UnixNano()
	}
	if r.ParentRunID != "" {
		parent = r.ParentRunID
	}
	query := `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_at,started_at,finished_at,parent_run_id,retry_of,replay_of_run_id,replay_of_node_id,trigger_source,git_branch,git_sha,error,created_principal,log_retention_class,log_retention_days)
SELECT ?,?,COALESCE(MAX(number),0)+1,?,?,?,?,?,'gh:1',?,'host','execute',?,?,?,?,?,?,?,?,?,?,?,?,'fixture','r30',30 FROM runs WHERE team=?`
	if s.Dialect() == store.DialectPostgres {
		for i := 1; strings.Contains(query, "?"); i++ {
			query = strings.Replace(query, "?", fmt.Sprintf("$%d", i), 1)
		}
	}
	_, err = Exec(ctx, s, query, team, r.ID, r.Pipeline, r.Status, body, "fixture", created.UnixNano(), repository, []byte(`[]`), created.UnixNano(), started, finished, parent, r.RetryOf, r.ReplayOfRunID, r.ReplayOfNodeID, r.TriggerSource, r.GitBranch, r.GitSHA, r.Error, team)
	return err
}

// SeedRunFinish sets terminal state for fixtures that do not exercise run execution.
func SeedRunFinish(s *store.Store, ctx context.Context, team store.Team, id, status, message string) error {
	query := `UPDATE runs SET status=?,error=?,finished_at=? WHERE team=? AND id=?`
	if s.Dialect() == store.DialectPostgres {
		query = `UPDATE runs SET status=$1,error=$2,finished_at=$3 WHERE team=$4 AND id=$5`
	}
	result, err := Exec(ctx, s, query, status, message, time.Now().UnixNano(), team, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("fixture run %s/%s: matched %d rows", team, id, n)
	}
	return nil
}

// SeedNodeFinish sets terminal facts for fixtures that do not exercise job execution.
func SeedNodeFinish(s *store.Store, ctx context.Context, team store.Team, ref store.NodeRef, result store.NodeResult) error {
	query := `UPDATE nodes SET status='done',outcome=?,error=?,failure_reason=?,exit_code=?,finished_at=?,
claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',owner_capability_prefix=''
WHERE team=? AND run_id=? AND node_id=?`
	if s.Dialect() == store.DialectPostgres {
		query = `UPDATE nodes SET status='done',outcome=$1,error=$2,failure_reason=$3,exit_code=$4,finished_at=$5,
claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',owner_capability_prefix=''
WHERE team=$6 AND run_id=$7 AND node_id=$8`
	}
	write, err := Exec(ctx, s, query, result.Outcome, result.MaskedError, result.Reason, result.ExitCode, time.Now().UnixNano(), team, ref.RunID, ref.NodeID)
	if err != nil {
		return err
	}
	n, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("fixture job %s/%s/%s: matched %d rows", team, ref.RunID, ref.NodeID, n)
	}
	return nil
}

// SeedNode adds reporting facts without admitting or executing work.
func SeedNode(s *store.Store, ctx context.Context, n store.Node) error {
	deps, err := json.Marshal(n.Deps)
	if err != nil {
		return err
	}
	spec, err := json.Marshal(nodeproto.JobSpec{ID: n.NodeID, Needs: n.Deps, Requires: n.NeedsLabels, Prefers: n.PrefersLabels})
	if err != nil {
		return err
	}
	query := `INSERT INTO nodes(team,run_id,node_id,status,outcome,deps_json,started_at,finished_at,spec_json,spec_hash,execution_json,attempt,requested_cores,requested_memory_bytes,requested_slots,seq)
SELECT team,id,?,?,?,?,?,?,?,?,?,?,?,?,?,(SELECT COALESCE(MAX(seq),0)+1 FROM nodes WHERE run_id=?) FROM runs WHERE id=?`
	if s.Dialect() == store.DialectPostgres {
		for i := 1; strings.Contains(query, "?"); i++ {
			query = strings.Replace(query, "?", fmt.Sprintf("$%d", i), 1)
		}
	}
	var started any
	if n.StartedAt != nil {
		started = n.StartedAt.UnixNano()
	}
	var finished any
	if n.FinishedAt != nil {
		finished = n.FinishedAt.UnixNano()
	}
	write, err := Exec(ctx, s, query, n.NodeID, n.Status, n.Outcome, deps, started, finished, spec, n.SpecHash, []byte(`{}`), n.AttemptsConsumed, n.RequestedCores, n.RequestedMemoryBytes, max(n.RequestedSlots, 1), n.RunID, n.RunID)
	if err != nil {
		return err
	}
	count, err := write.RowsAffected()
	if err == nil && count != 1 {
		return fmt.Errorf("fixture job %s/%s: matched %d runs", n.RunID, n.NodeID, count)
	}
	return err
}

// SeedNodeReady sets queue timestamps on an existing reporting fixture.
func SeedNodeReady(s *store.Store, ctx context.Context, run, node string) error {
	query := `UPDATE nodes SET ready_at=COALESCE(ready_at,?),queue_started_at=COALESCE(queue_started_at,?) WHERE run_id=? AND node_id=?`
	if s.Dialect() == store.DialectPostgres {
		query = `UPDATE nodes SET ready_at=COALESCE(ready_at,$1),queue_started_at=COALESCE(queue_started_at,$2) WHERE run_id=$3 AND node_id=$4`
	}
	now := time.Now().UnixNano()
	write, err := Exec(ctx, s, query, now, now, run, node)
	if err != nil {
		return err
	}
	count, err := write.RowsAffected()
	if err == nil && count != 1 {
		return fmt.Errorf("fixture ready %s/%s: matched %d rows", run, node, count)
	}
	return err
}
