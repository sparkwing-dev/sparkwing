package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestClaimSearchBoundsRejectedBacklogAndRechecksFairShare(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	var raw []byte
	if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(raw, &sealed); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	sealed.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}, OverflowMinutes: &zero}
	raw, _ = json.Marshal(sealed)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
	for i := range 80 {
		id := fmt.Sprintf("capped-%03d", i)
		spec, _ := json.Marshal(map[string]string{"id": id})
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,'pending',1,?,execution_json FROM nodes WHERE team='a' AND node_id='job'`, id, spec)
		indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: runspec.NodeID(id)})
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,'next','pending',2,?,execution_json FROM nodes WHERE team='b' AND node_id='job'`, []byte(`{"id":"next"}`))
	indexFixtureUnit(t, s, "b", UnitRef{Kind: capability.UnitNode, RunID: "run-b", NodeID: "next"})
	// Replay through the SQL tracer to measure work, independent of machine speed.
	path := filepath.Join(t.TempDir(), "claims.db")
	rewriteSchemaExec(t, s.db, `VACUUM INTO ?`, path)
	trace := &claimBenchmarkTrace{enabled: true}
	name := path
	sql.Register(name, &claimBenchmarkDriver{Driver: s.db.Driver(), trace: trace})
	db, err := sql.Open(name, sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s.db = db
	for poll := range 2 {
		trace.statements = nil
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil || claim.Team != "b" {
			t.Fatalf("poll %d: capped team hid healthy work: %+v %v", poll, claim, err)
		}
		queries := 0
		for _, statement := range trace.statements {
			if strings.HasPrefix(statement.query, "WITH RECURSIVE clock(") {
				queries++
			}
		}
		if queries > 10 {
			t.Fatalf("poll %d issued %d eligibility queries; want at most 10", poll, queries)
		}
	}
}
