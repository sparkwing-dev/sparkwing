package store

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// AddIdleTeams creates n live teams that hold no work, each with a shared runner of its own.
func (f *RankingFixture) AddIdleTeams(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		team := fmt.Sprintf("idle-%02d", i)
		f.Store.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`, team)
		root := authorityRoot("runner-"+team, team)
		root.Digest = sha256.Sum256([]byte("idle-runner-" + team))
		root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
		authorityPersist(t, f.Store, root)
		f.idleRunners = append(f.idleRunners, root.Prefix)
	}
}

// PollIdleRunners records a live poll from every idle team's runner at the current clock.
func (f *RankingFixture) PollIdleRunners(t *testing.T) {
	t.Helper()
	for _, prefix := range f.idleRunners {
		f.Poll(t, prefix, capability.UnitNode)
	}
}

// SetPaymentMode configures the store's payment mode.
func (f *RankingFixture) SetPaymentMode(t *testing.T, mode capability.PaymentMode) {
	t.Helper()
	f.Store.testOnlyExec(t, `INSERT INTO settings(key,value) VALUES ('payment_mode',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, []byte(`"`+string(mode)+`"`))
}

// Fund grants team a free credits in the current payment mode.
func (f *RankingFixture) Fund(t *testing.T) {
	t.Helper()
	fundUnitClaim(t, f.Store)
}

// QueueEvalOnly finishes the plan unit and the job and queues the job's evaluation, so only an eval unit waits.
func (f *RankingFixture) QueueEvalOnly(t *testing.T) {
	t.Helper()
	f.Store.testOnlyExec(t, `UPDATE runs SET dispatch='controller'`)
	f.Store.testOnlyExec(t, `UPDATE triggers SET status='done',finished_at=?`, f.ReadyAt.UnixNano())
	f.Store.testOnlyExec(t, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
	f.Store.testOnlyExec(t, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','job',7,'pending')`)
}

// ClaimTeams lists, in fairness order, the teams a Cloud launcher search over kinds builds facts for.
func (f *RankingFixture) ClaimTeams(t *testing.T, kinds ...capability.UnitKind) []string {
	t.Helper()
	tx, err := f.Store.beginSnapshotReadTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	listed, err := claimTeamsTx(t.Context(), tx, "", kinds)
	if err != nil {
		t.Fatal(err)
	}
	var teams []string
	for _, team := range listed {
		teams = append(teams, team.Team)
	}
	return teams
}

// SearchOnce runs one Cloud launcher claim search over kinds, without the award transaction.
func (f *RankingFixture) SearchOnce(t *testing.T, kinds ...capability.UnitKind) error {
	t.Helper()
	_, err := f.Store.AsOperator().searchClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: f.Launcher, Kinds: kinds, Labels: []string{"team-4cpu"}})
	return err
}

// ReadyJobAgain marks a requeued job of the fixture run ready at the current clock, as its plan's release does.
func (f *RankingFixture) ReadyJobAgain(t *testing.T, id string) {
	t.Helper()
	f.Store.testOnlyExec(t, `UPDATE nodes SET ready_at=?,queue_started_at=COALESCE(queue_started_at,?) WHERE team='a' AND run_id='run' AND node_id=?`, f.now.UnixNano(), f.now.UnixNano(), id)
}

// SeedSkewedBacklog adds 48 live teams. Ahead of one eligible unit, skew-00 holds backlog approval-pending, claimed
// and cancelled jobs, skew-01 cancelled and claimed plans, and skew-03 claimed and finished evals; skew-02 holds
// backlog trigger-kind units. skew-04, skew-05 and skew-06 hold only rejected jobs, plans and evals; the rest nothing.
func SeedSkewedBacklog(t *testing.T, s *Store, backlog int) {
	t.Helper()
	for i := range 48 {
		s.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`, fmt.Sprintf("skew-%02d", i))
	}
	series := `WITH RECURSIVE series(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM series WHERE i<?) `
	numbers := map[string]int{}
	runs := func(team, prefix string, n int) {
		prefix = team + "-" + prefix
		first := numbers[team]
		numbers[team] += n
		s.testOnlyExec(t, series+`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) SELECT ?,CAST(? AS TEXT)||i,i+?,'ci','pending',?,'request',1,'gh:1','https://github.com/acme/repo','controller','execute','account',1,'r30',30 FROM series`, n, team, prefix, first, []byte(`{}`))
	}
	nodes := func(team, tag, columns, values string, n int) {
		s.testOnlyExec(t, series+`INSERT INTO nodes(team,run_id,node_id,spec_json,execution_json,`+columns+`) SELECT ?,?,CAST(? AS TEXT)||i,?,?,`+values+` FROM series`, n, team, team+"-jobs-1", tag, []byte(`{}`), []byte(`{}`))
	}
	triggers := func(team, prefix, columns, values string, n int) {
		runs(team, prefix, n)
		prefix = team + "-" + prefix
		s.testOnlyExec(t, series+`INSERT INTO triggers(team,id,available_at,created_at,needs_labels,prefers_labels,`+columns+`) SELECT ?,CAST(? AS TEXT)||i,1,1,?,?,`+values+` FROM series`, n, team, prefix, []byte(`[]`), []byte(`[]`))
	}
	evals := func(team, columns, values string, n, offset int) {
		s.testOnlyExec(t, series+`INSERT INTO eval_units(team,run_id,source_node_id,source_generation,`+columns+`) SELECT ?,?,'job',i+?,`+values+` FROM series`, n, team, team+"-evals-1", offset)
	}
	claimed := `claimed_by,capability_prefix,lease_expires_at,generation`
	held := `'holder','held-'||i,1,1`
	for _, team := range []string{"skew-00", "skew-04"} {
		runs(team, "jobs-", 1)
		nodes(team, "approval-", `status,ready_at`, `'approval_pending',1`, backlog)
	}
	nodes("skew-00", "claimed-", `status,ready_at,`+claimed, `'pending',1,`+held, backlog)
	nodes("skew-00", "cancelled-", `status,ready_at,cancel_requested_at`, `'pending',1,1`, backlog)
	nodes("skew-00", "eligible-", `status,ready_at`, `'pending',2`, 1)
	for _, team := range []string{"skew-01", "skew-05"} {
		triggers(team, "cancelled-", `status,unit_kind,cancel_requested_at`, `'pending','plan',1`, backlog)
	}
	triggers("skew-01", "claimed-", `status,unit_kind,claimed_at,`+claimed, `'claimed','plan',1,`+held, backlog)
	triggers("skew-01", "plan-", `status,unit_kind`, `'pending','plan'`, 1)
	triggers("skew-02", "trigger-", `status,unit_kind`, `'pending','trigger'`, backlog)
	for _, team := range []string{"skew-03", "skew-06"} {
		runs(team, "evals-", 1)
		s.testOnlyExec(t, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES (?,?,'job','pending',?,?)`, team, team+"-evals-1", []byte(`{}`), []byte(`{}`))
		evals(team, `status,finished_at`, `'done',1`, backlog, 0)
	}
	evals("skew-03", `status,claimed_at,`+claimed, `'claimed',1,`+held, backlog, backlog)
	evals("skew-03", `status`, `'pending'`, 1, 2*backlog)
	// hack: PostgreSQL's autovacuum gathers planner statistics in production and the store never analyzes SQLite.
	if s.dialect == DialectPostgres {
		s.testOnlyExec(t, `ANALYZE`)
	}
}
