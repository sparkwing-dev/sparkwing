package store

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestClaimRoutesSeekBoundedHeadsInALargeQueue(t *testing.T) {
	var visited atomic.Int64
	probe := fmt.Sprintf("route_probe_%x", time.Now().UnixNano())
	if err := sqlite.RegisterScalarFunction(probe, 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		visited.Add(1)
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	s, demand := unitClaimFixture(t)
	demand.Target = nil
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		requirements, err := persistClaimRequirementsTx(t.Context(), tx, "a", []string{"sparkwing-4cpu"})
		if err != nil {
			return err
		}
		var execution []byte
		if err := tx.QueryRowContext(t.Context(), `SELECT execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&execution); err != nil {
			return err
		}
		claimContext, err := persistClaimContextTx(t.Context(), tx, "a", "run", capability.UnitNode, requirements, execution)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), `UPDATE nodes SET claim_requirements=?,claim_context=? WHERE team='a'`, requirements, claimContext); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), `WITH RECURSIVE sequence(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM sequence WHERE i<50000) INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) SELECT 'a','run','old-'||i,'pending',1,spec_json,execution_json,claim_requirements,claim_context FROM sequence,nodes WHERE team='a' AND node_id='job'`); err != nil {
			return err
		}
		q := claimSQL{}
		if err := claimInputsTx(t.Context(), tx, demand, demand.Kinds, &q); err != nil {
			return err
		}
		query, args, err := q.routesSQL()
		if err != nil {
			return err
		}
		query = strings.ReplaceAll(query, `n.status='pending'`, probe+`(n.node_id)=1 AND n.status='pending'`)
		rows, err := tx.QueryContext(t.Context(), query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var team, requirements, context string
			var ordinal int64
			var head sql.NullString
			if err := rows.Scan(&team, &requirements, &ordinal, &context, &head); err != nil {
				return err
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if got := visited.Load(); got != 1 {
		t.Fatalf("route heads visited %d nodes; want one indexed head visit", got)
	}
}

func TestCloudNamedRecheckSeeksTheTenantUnitKey(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		q := claimSQL{}
		query, err := claimQueryTx(t.Context(), tx, demand, demand.Kinds, &q)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+query, q.args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var plan []string
		found := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			plan = append(plan, detail)
			if strings.Contains(detail, "SEARCH n ") && strings.Contains(detail, "team=? AND run_id=? AND node_id=?") {
				found = true
			}
			if strings.HasPrefix(detail, "SCAN n") {
				t.Errorf("named recheck scans nodes: %s", detail)
			}
		}
		if !found {
			t.Errorf("named recheck lacks a complete tenant/unit index seek: %v", plan)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClaimPredicateJoinsOnlyTheTeamsClaimants(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	facts := []claimFacts{{Prefix: demand.CapabilityPrefix, Current: 1, Cloud: 1, Trust: "any", Kinds: []string{"node"}, Labels: []string{"sparkwing-4cpu"}}}
	teams := make([]claimTeamFacts, 200)
	for i := range teams {
		teams[i] = claimTeamFacts{Team: fmt.Sprintf("team-%03d", i), Balance: 1e12, Available: 128}
	}
	for i := range 999 {
		facts = append(facts, claimFacts{Prefix: fmt.Sprintf("observer-%04d", i), Team: teams[i%len(teams)].Team, Trust: "any", Kinds: []string{"node"}})
	}
	q := claimSQL{dialect: DialectSQLite}
	query, err := q.query(facts, teams, DefaultCreditRateTable(DefaultCreditRateMicro), []capability.UnitKind{capability.UnitNode}, demand, s.now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+query, q.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
		if (strings.Contains(detail, "SEARCH c USING AUTOMATIC") || strings.Contains(detail, "SEARCH w USING AUTOMATIC")) && strings.Contains(detail, "(team=?)") {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("claimant scope lacks a team index seek: %v", plan)
	}
}
