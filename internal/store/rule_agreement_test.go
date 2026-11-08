package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestGoSQLRuleAgreement(t *testing.T) {
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		t.Run(dialect.String(), func(t *testing.T) {
			driver, dsn := "sqlite", ":memory:"
			if dialect == DialectPostgres {
				driver, dsn = "pgx", os.Getenv("SPARKWING_TEST_PG_URL")
				if dsn == "" {
					t.Skip("PostgreSQL agreement requires SPARKWING_TEST_PG_URL")
				}
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			db.SetMaxOpenConns(1)
			if dialect == DialectPostgres {
				schema := fmt.Sprintf("rule_agreement_%d", time.Now().UnixNano())
				if _, err := db.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
						t.Error(err)
					}
				})
				if _, err := db.ExecContext(t.Context(), `SET search_path TO `+schema); err != nil {
					t.Fatal(err)
				}
			} else {
				rewriteSchemaExec(t, db, `PRAGMA foreign_keys = ON`)
			}
			schema, err := translateSchema(schemaNeutral, dialect)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), schema); err != nil {
				t.Fatal(err)
			}
			t.Run("Actions tenant correlations", func(t *testing.T) { actionsSQLTenantCorrelations(t, db, dialect) })
			t.Run("overflow budget", func(t *testing.T) { overflowBudgetAgreement(t, db, dialect) })
			t.Run("repository tiers", func(t *testing.T) { repositoryTiersAgreement(t, db, dialect) })
			t.Run("tier eligibility", func(t *testing.T) { repositoryTierMatchAgreement(t, db, dialect) })
			t.Run("placement holds", func(t *testing.T) { placementHoldsAgreement(t, db, dialect) })
			t.Run("holder budget", func(t *testing.T) { holderBudgetAgreement(t, db, dialect) })
		})
	}
}

func TestRetryReuseGoSQLAgreement(t *testing.T) {
	for _, mode := range []string{"failed", "all"} {
		for _, status := range []runstate.NodeStatus{runstate.StatusPending, runstate.StatusRunning, runstate.StatusDone} {
			for _, outcome := range []runstate.NodeState{"", runstate.NodeSuccess, runstate.NodeMemoized, runstate.NodeFailed, runstate.NodeSkipped, runstate.NodeCancelled, runstate.NodeSuperseded} {
				if (status == runstate.StatusDone) != (outcome != "") {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/%s", mode, status, outcome), func(t *testing.T) {
					tenant, _ := materializationFixture(t, runstate.SourceManualRetry)
					var finished, holder, lease any
					prefix := ""
					if status == runstate.StatusDone {
						finished = int64(1)
					}
					if status == runstate.StatusRunning {
						holder, prefix, lease = "past", "past", int64(42)
					}
					rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status=?,outcome=?,finished_at=?,started_at=1,claimed_by=?,capability_prefix=?,lease_expires_at=? WHERE team='a' AND run_id='source'`, status, outcome, finished, holder, prefix, lease)
					err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
						selection := runstate.RetrySelection{Mode: mode}
						if err := selectRetryTx(t.Context(), tx, tenant.team, "source", &selection); err != nil {
							return err
						}
						run, err := readRunTx(t.Context(), tx, tenant.team, "run")
						if err != nil {
							return err
						}
						run.Retry.Mode = mode
						target, err := readNodeTx(t.Context(), tx, tenant.team, runspec.NodeRef{RunID: "run", NodeID: "job"})
						if err != nil {
							return err
						}
						source := runstate.MaterializedResultSource{Ref: runspec.NodeRef{RunID: "source", NodeID: "job"}, StateSeq: 1}
						_, goErr := materializationResultTx(t.Context(), tx, tenant.team, run, target, runstate.MaterializeSource{Kind: runstate.SourceManualRetry, Result: &source})
						selected, sqlOK := selection.Results["job"]
						if sqlOK != (goErr == nil) || sqlOK && selected != source {
							t.Errorf("SQL selected=%+v present=%v; Go materialization error=%v", selected, sqlOK, goErr)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func agreementJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "null" {
		return "[]"
	}
	return string(raw)
}

func repositoryTiersAgreement(t *testing.T, db *sql.DB, dialect Dialect) {
	pipeline := `[{"target":"team","labels":["gpu"],"hold":5000000000},{"target":"cloud","hold":0}]`
	for _, test := range []struct {
		name, placement string
	}{
		{"missing", ``},
		{"null policy", `null`},
		{"empty policy", `{}`},
		{"null pipeline", `{"prefer":null}`},
		{"empty pipeline", `{"prefer":[]}`},
		{"pipeline", `{"prefer":` + pipeline + `}`},
		{"other job", `{"prefer":` + pipeline + `,"jobs":[{"job":"other","prefer":[]}]}`},
		{"missing override list", `{"prefer":` + pipeline + `,"jobs":[{"job":"job"}]}`},
		{"null override", `{"prefer":` + pipeline + `,"jobs":[{"job":"job","prefer":null}]}`},
		{"empty override", `{"prefer":` + pipeline + `,"jobs":[{"job":"job","prefer":[]}]}`},
		{"own tiers", `{"prefer":` + pipeline + `,"jobs":[{"job":"job","prefer":[{"target":"name=mac","hold":7}]}]}`},
		{"override without pipeline", `{"jobs":[{"job":"job","prefer":` + pipeline + `}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := `{"request":{}}`
			if test.placement != "" {
				request = `{"request":{"placement":` + test.placement + `}}`
			}
			var seal struct {
				Request struct {
					Placement *runspec.RepositoryPlacement `json:"placement"`
				} `json:"request"`
			}
			if err := json.Unmarshal([]byte(request), &seal); err != nil {
				t.Fatal(err)
			}
			q := claimSQL{dialect: dialect}
			query := `WITH w(request,node_id,kind) AS (SELECT CAST(? AS TEXT),'job','node') SELECT ` + q.repositoryTiers("w") + ` FROM w`
			var raw sql.NullString
			if err := db.QueryRowContext(t.Context(), rewritePh(dialect, query), request).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var got []runspec.RepositoryTier
			if raw.Valid {
				if err := json.Unmarshal([]byte(raw.String), &got); err != nil {
					t.Fatal(err)
				}
			}
			want := seal.Request.Placement.ForJob("job")
			if !slices.EqualFunc(got, want, func(a, b runspec.RepositoryTier) bool { return reflect.DeepEqual(a, b) }) {
				t.Fatalf("SQL tiers=%+v, ForJob=%+v", got, want)
			}
		})
	}
}

func repositoryTierMatchAgreement(t *testing.T, db *sql.DB, dialect Dialect) {
	tiers := []runspec.RepositoryTier{
		{Target: runspec.PlacementTeam},
		{Target: runspec.PlacementTeam, Labels: []string{"os=linux,os=darwin", "gpu"}},
		{Target: runspec.PlacementTeam, Labels: []string{"\u2003gpu\u2003"}},
		{Target: runspec.PlacementTeam, Labels: []string{`a"b,a\b`}},
		{Target: "name=mac"},
		{Target: "name=mac", Labels: []string{"gpu"}},
		{Target: runspec.PlacementCloud},
		{Target: runspec.PlacementGitHubActions},
	}
	facts := []claimFacts{
		{},
		{Tier: tierSelfHosted},
		{Tier: tierSelfHosted, Name: "mac", Labels: []string{"os=darwin", "gpu", `a"b`}},
		{Tier: tierSelfHosted, Name: "linux", Labels: []string{"os=linux"}},
		{Tier: tierSelfHosted, Name: "mac", Labels: []string{`a\b`}},
		{Tier: tierPersonal, Name: "mac", Labels: []string{"os=darwin", "gpu"}},
		{Tier: TierCloud, Cloud: 1, Name: "mac", Labels: []string{"gpu"}},
		{Tier: tierActions, Name: "mac", Labels: []string{"gpu"}},
	}
	for i, tier := range tiers {
		for j, fact := range facts {
			t.Run(fmt.Sprintf("tier=%d/facts=%d", i, j), func(t *testing.T) {
				q := claimSQL{dialect: dialect}
				query := `WITH s(target,selectors,runner,workflow) AS (SELECT CAST(? AS TEXT),CAST(? AS TEXT),CAST(? AS TEXT),CAST(? AS TEXT)),c(facts) AS (SELECT CAST(? AS TEXT)) SELECT ` + q.repositoryTierMatch("s", "c.facts") + ` FROM s CROSS JOIN c`
				var got bool
				if err := db.QueryRowContext(t.Context(), rewritePh(dialect, query), "repo:"+tier.Target, agreementJSON(t, tier.Labels), tier.Runner, tier.Workflow, agreementJSON(t, fact)).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if want := repositoryTierMatches(tier, fact); got != want {
					t.Fatalf("tier=%+v facts=%+v: SQL=%v Go=%v", tier, fact, got, want)
				}
			})
		}
	}
	for _, selectors := range [][]string{nil, {}, {""}, {"\u2003"}, {","}, {"gpu,cpu", "linux"}, {"gpu", "missing"}, {`a"b,a\b`}} {
		for _, labels := range [][]string{nil, {"gpu"}, {"cpu", "linux"}, {`a"b`}, {`a\b`}} {
			t.Run(fmt.Sprintf("selectors=%q/labels=%q", selectors, labels), func(t *testing.T) {
				q := claimSQL{dialect: dialect}
				predicate := q.terms("s.selectors", func(alt string) string { return q.contains("s.labels", alt) })
				query := `WITH s(selectors,labels) AS (SELECT CAST(? AS TEXT),CAST(? AS TEXT)) SELECT ` + predicate + ` FROM s`
				var got bool
				if err := db.QueryRowContext(t.Context(), rewritePh(dialect, query), agreementJSON(t, selectors), agreementJSON(t, labels)).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if want := match.Evaluate(match.Profile{Labels: labels}, match.Demand{Selector: selectors}).OK(); got != want {
					t.Fatalf("SQL=%v match.Evaluate=%v", got, want)
				}
			})
		}
	}
}

func holderBudgetAgreement(t *testing.T, db *sql.DB, dialect Dialect) {
	for _, superseded := range []bool{false, true} {
		for _, delta := range []int64{-1, 0, 1} {
			t.Run(fmt.Sprintf("superseded=%v/expiry=%d", superseded, delta), func(t *testing.T) {
				const now = int64(1_700_000_000_000_000_000)
				query := `WITH h(superseded,lease_expires_at) AS (SELECT CAST(? AS BIGINT),CAST(? AS BIGINT)) SELECT ` + holderLiveSQL("h.") + ` FROM h`
				var got bool
				if err := db.QueryRowContext(t.Context(), rewritePh(dialect, query), boolInt(superseded), now+delta, now).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if want := holderCountsForBudget(superseded, now+delta, now); got != want {
					t.Fatalf("SQL=%v holderCountsForBudget=%v", got, want)
				}
			})
		}
	}
}

func placementHoldsAgreement(t *testing.T, db *sql.DB, dialect Dialect) {
	ready := time.Unix(1_700_000_000, 0)
	tiers := []runspec.RepositoryTier{
		{Target: "name=mac", Hold: 5 * time.Second},
		{Target: runspec.PlacementTeam, Hold: 3 * time.Second},
		{Target: runspec.PlacementCloud},
	}
	mac := placementCandidate{name: "mac", facts: claimFacts{Tier: tierSelfHosted, Name: "mac", FreeSlots: 1}}
	linux := placementCandidate{name: "linux", facts: claimFacts{Tier: tierSelfHosted, Name: "linux", FreeSlots: 1}}
	cloud := placementCandidate{name: "cloud", facts: claimFacts{Tier: TierCloud, Cloud: 1, FreeSlots: 1}}
	busy := mac
	busy.facts.FreeSlots = 0
	refused := mac
	refused.standing.failed = stepBilling
	for _, test := range []struct {
		name       string
		candidates []placementCandidate
		ranking    []string
	}{
		{"all live", []placementCandidate{mac, linux, cloud}, nil},
		{"earliest match only", []placementCandidate{mac, cloud}, nil},
		{"missing first", []placementCandidate{linux, cloud}, nil},
		{"busy first", []placementCandidate{busy, linux, cloud}, nil},
		{"refused first", []placementCandidate{refused, cloud}, nil},
		{"ranking excludes team", []placementCandidate{mac, linux, cloud}, []string{TierCloud}},
		{"empty ranking", []placementCandidate{mac, linux, cloud}, []string{}},
	} {
		for _, elapsed := range []time.Duration{0, 5*time.Second - 1, 5 * time.Second, 5*time.Second + 1, 8*time.Second - 1, 8 * time.Second} {
			t.Run(fmt.Sprintf("%s/elapsed=%s", test.name, elapsed), func(t *testing.T) {
				waits := placementTierWaits(tiers, test.candidates, test.ranking, ready, ready.Add(elapsed))
				q := claimSQL{dialect: dialect}
				ranking := ""
				if test.ranking != nil {
					ranking = agreementJSON(t, test.ranking)
				}
				query := `WITH clock(now) AS (SELECT CAST(` + q.param(ready.Add(elapsed).UnixNano()) + ` AS BIGINT)),work AS (SELECT 'a' AS team,'run' AS run_id,'job' AS node_id,0 AS source_generation,'node' AS kind,CAST(` + q.param(`{"request":{}}`) + ` AS TEXT) AS request,1 AS repo,CAST(` + q.param(agreementJSON(t, tiers)) + ` AS TEXT) AS repo_tiers,'[]' AS preferences,0 AS run_anywhere,0 AS pinned,CAST(` + q.param(ranking) + ` AS TEXT) AS claim_ranking,CAST(` + q.param(ready.UnixNano()) + ` AS BIGINT) AS placed,CAST(` + q.param(ready.UnixNano()) + ` AS BIGINT) AS arrived,1 AS cpu_class),eligible AS (`
				var inputs []string
				for _, candidate := range test.candidates {
					if candidate.standing.ok() {
						inputs = append(inputs, `SELECT work.*,CAST(`+q.param(agreementJSON(t, candidate.facts))+` AS TEXT) AS facts FROM work`)
					}
				}
				query += strings.Join(inputs, " UNION ALL ") + `)`
				ctes, err := q.placementCTEs(ClaimPlacement{})
				if err != nil {
					t.Fatal(err)
				}
				query += ctes + ` SELECT EXISTS (SELECT 1 FROM available a WHERE ` + q.number(q.at("a.facts", "cloud")) + `=1)`
				var got bool
				if err := db.QueryRowContext(t.Context(), rewritePh(dialect, query), q.args...).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if want := waits[2].OpensIn == 0; got != want {
					t.Fatalf("SQL cloud available=%v, Go opens in %s", got, waits[2].OpensIn)
				}
			})
		}
	}
}

func overflowBudgetAgreement(t *testing.T, db *sql.DB, dialect Dialect) {
	for _, limits := range []struct {
		name             string
		monthly, request *int64
	}{
		{"unlimited", nil, nil},
		{"monthly only", new(int64(3)), nil},
		{"request only", nil, new(int64(3))},
		{"monthly lower", new(int64(3)), new(int64(4))},
		{"request lower", new(int64(4)), new(int64(3))},
		{"equal limits", new(int64(3)), new(int64(3))},
		{"monthly zero", new(int64(0)), nil},
		{"request zero", nil, new(int64(0))},
	} {
		for _, used := range []int64{0, 180 - SliceSeconds - 1, 180 - SliceSeconds, 180 - SliceSeconds + 1, 179, 180, 181, 240} {
			t.Run(fmt.Sprintf("%s/used=%d", limits.name, used), func(t *testing.T) {
				now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
				s := &Store{db: db, dialect: dialect, pinnedClock: func() time.Time { return now }}
				tx, err := s.beginTx(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				exec := func(query string, args ...any) {
					t.Helper()
					if _, err := tx.tx.ExecContext(t.Context(), rewritePh(dialect, query), args...); err != nil {
						t.Fatal(err)
					}
				}
				exec(`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
				exec(`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('owner','owner@example.test',1,1)`)
				request := runspec.SealedRequest{Request: runspec.Request{Burst: &runspec.BurstPlacement{OverflowMinutes: limits.request}}}
				body, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'p','pending',?,'request',1,'gh:1','host','execute','owner',1,'r30',30)`, body)
				if limits.monthly != nil {
					exec(`INSERT INTO overflow_controls(team,account_id,monthly_minutes) VALUES ('a','owner',?)`, *limits.monthly)
				}
				exec(`INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,requester_account) VALUES ('a','award','cloud','run',1,'a',1,'cloud','at_start','node','job',?,'owner')`, now.UnixNano())
				exec(`INSERT INTO credit_charges(team,id,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('a','reservation','run','job','cloud','reservation',?,1,?)`, used, now.UnixNano())
				exhausted, control, err := overflowExhaustedTx(t.Context(), tx, "a", "run", SliceSeconds)
				if err != nil {
					t.Fatal(err)
				}
				if control.UsedSeconds != used {
					t.Fatalf("Go usage=%d, seeded reservation=%d", control.UsedSeconds, used)
				}
				facts, err := claimOverflowUsageTx(t.Context(), tx, []claimOverflowFacts{{Team: "a", Account: "owner"}})
				if err != nil {
					t.Fatal(err)
				}
				q := claimSQL{dialect: dialect, overflow: facts}
				cte, err := q.overflowFactsSQL()
				if err != nil {
					t.Fatal(err)
				}
				minutes := "CAST(" + q.param(limits.request) + " AS BIGINT)"
				query := `WITH ` + cte + `,budget(minutes) AS (SELECT ` + minutes + `) SELECT ` + q.overflowBudgetAllowedSQL("'a'", "'owner'", "budget.minutes", "1") + ` FROM budget`
				var allowed bool
				if err := tx.QueryRowContext(t.Context(), query, q.args...).Scan(&allowed); err != nil {
					t.Fatal(err)
				}
				if allowed == exhausted {
					t.Fatalf("monthly=%v request=%v used=%d reservation=%d: SQL allowed=%v Go exhausted=%v", limits.monthly, limits.request, used, SliceSeconds, allowed, exhausted)
				}
			})
		}
	}
}

func actionsSQLTenantCorrelations(t *testing.T, db *sql.DB, dialect Dialect) {
	for _, statement := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES('actions-a',1,1),('actions-b',1,1)`,
		`INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES(41,'actions-a',7,'acme','Organization',1,1)`,
		`INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_at,repo_key,installation_id) VALUES('actions-a',42,7,'acme/repo',1,'gh:42',41)`,
		`INSERT INTO github_app_automation(team,repository_id,repository,installation_id,enabled_by,enabled_at,repo_key) VALUES('actions-a',42,'acme/repo',41,'owner',1,'gh:42')`,
		`INSERT INTO github_actions_approvals VALUES('actions-a',42,'.github/workflows/ci.yaml','ubuntu-latest','refs/heads/main','approval')`,
		`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES('actions-a','actions-run',1,'ci','pending',X'7b7d','hash',1,'gh:42','controller','execute','owner',1,'standard',7)`,
		`INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('actions-a','actions-run','job','pending',1,X'7b7d',X'7b7d')`,
		`INSERT INTO github_actions_dispatches(receipt,team,run_id,node_id,queue_started_at,generation,approval_id,installation_id,repository_id,repository_owner_id,repository,workflow,workflow_ref,runner,policy_revision,state,provider_run_id,provider_attempt) VALUES('actions-receipt','actions-a','actions-run','job',1,1,'approval',41,42,7,'acme/repo','.github/workflows/ci.yaml','refs/heads/main','ubuntu-latest','policy','accepted','provider-run','1')`,
	} {
		if dialect == DialectPostgres {
			statement = strings.ReplaceAll(statement, "X'7b7d'", "decode('7b7d','hex')")
		}
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	push := capability.PushScope{Receipt: "actions-receipt", WorkflowRunID: "provider-run", WorkflowRunAttempt: "1", RunnerEnvironment: "github-hosted", RunnerImage: "ubuntu-latest"}
	for _, team := range []string{"actions-a", "actions-b"} {
		q := claimSQL{dialect: dialect}
		raw, err := q.jsonParam(claimFacts{Team: team, Tier: tierActions, Pushes: []capability.PushScope{push}})
		if err != nil {
			t.Fatal(err)
		}
		query := `WITH s(target,selectors,runner,workflow) AS (SELECT 'repo:github-actions','[]','ubuntu-latest','.github/workflows/ci.yaml'),c(facts) AS (SELECT ` + raw + `) SELECT ` + q.repositoryTierMatch("s", "c.facts") + ` FROM s CROSS JOIN c`
		var got bool
		if err := db.QueryRowContext(t.Context(), rewritePh(dialect, query), q.args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != (team == "actions-a") {
			t.Fatalf("tier receipt for team %s: %v", team, got)
		}
		q = claimSQL{dialect: dialect}
		raw, err = q.jsonParam(push)
		if err != nil {
			t.Fatal(err)
		}
		query = `SELECT ` + q.actionsReceiptScopeSQL("w.push", "w") + ` FROM (SELECT ` + raw + ` AS push,` + q.param(team) + ` AS team,'actions-run' AS run_id,'job' AS node_id,'node' AS kind) w`
		if err := db.QueryRowContext(t.Context(), rewritePh(dialect, query), q.args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != (team == "actions-a") {
			t.Fatalf("work receipt for team %s: %v", team, got)
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE github_actions_dispatches SET provider_run_id='',provider_attempt='',state='sending' WHERE receipt='actions-receipt'`); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, dialect: dialect}
	for _, team := range []string{"actions-b", "actions-a"} {
		spec := capability.ProvisionSpec{Team: team, Binding: capability.Binding{Push: &push}}
		spec.Binding.Push.RepositoryKey = "gh:42"
		err := s.inTx(t.Context(), func(tx *storeTx) error { return consumeActionsExchangeTx(t.Context(), tx, spec) })
		if (err == nil) != (team == "actions-a") {
			t.Fatalf("exchange for team %s: %v", team, err)
		}
	}
	var team string
	if err := db.QueryRowContext(t.Context(), `SELECT team FROM github_actions_exchanges WHERE run_id='provider-run'`).Scan(&team); err != nil || team != "actions-a" {
		t.Fatalf("exchange team %q: %v", team, err)
	}
}
