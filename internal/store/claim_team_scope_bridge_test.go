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

// SeedSkewedBacklog adds 48 live teams on PostgreSQL: skew-00 holds backlog ready jobs, skew-01 backlog pending plan
// units, skew-02 backlog pending trigger-kind units and skew-03 backlog pending eval units; the rest hold nothing.
func SeedSkewedBacklog(t testing.TB, s *Store, backlog int) {
	t.Helper()
	if s.dialect != DialectPostgres {
		t.Skip("the skewed backlog seeds through PostgreSQL's generate_series")
	}
	run := `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) SELECT $1,$2||i,i,'ci','pending',convert_to('{}','UTF8'),'request',1,'gh:1','https://github.com/acme/repo','controller','execute','account',1,'r30',30 FROM generate_series(1,$3::int) i`
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO teams(name,created_at,updated_at) SELECT 'skew-'||lpad(i::text,2,'0'),1,1 FROM generate_series(0,47) i`, nil},
		{run, []any{"skew-00", "jobs-", 1}},
		{`INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT 'skew-00','jobs-1','job-'||i,'pending',1,convert_to('{}','UTF8'),convert_to('{}','UTF8') FROM generate_series(1,$1::int) i`, []any{backlog}},
		{run, []any{"skew-01", "plans-", backlog}},
		{`INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels) SELECT 'skew-01','plans-'||i,'pending','plan',1,1,convert_to('[]','UTF8'),convert_to('[]','UTF8') FROM generate_series(1,$1::int) i`, []any{backlog}},
		{run, []any{"skew-02", "triggers-", backlog}},
		{`INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels) SELECT 'skew-02','triggers-'||i,'pending','trigger',1,1,convert_to('[]','UTF8'),convert_to('[]','UTF8') FROM generate_series(1,$1::int) i`, []any{backlog}},
		{run, []any{"skew-03", "evals-", 1}},
		{`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('skew-03','evals-1','job','pending',convert_to('{}','UTF8'),convert_to('{}','UTF8'))`, nil},
		{`INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) SELECT 'skew-03','evals-1','job',i,'pending' FROM generate_series(1,$1::int) i`, []any{backlog}},
		{`ANALYZE teams,runs,nodes,triggers,eval_units`, nil},
	} {
		if _, err := s.db.ExecContext(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.sql, err)
		}
	}
}
