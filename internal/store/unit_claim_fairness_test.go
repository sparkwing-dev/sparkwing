package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func launcherFairnessFixture(t *testing.T) (*Store, UnitDemand) {
	t.Helper()
	s, _ := unitClaimFixture(t)
	fundUnitClaim(t, s)
	if _, err := (&Tenant{s: s, team: "b"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 100000000, Reference: "claim-fixture-b", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	launcher := authorityRoot("launcher", "")
	launcher.Subject.Kind = capability.SubjectService
	launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
	expires := s.now().Add(time.Hour)
	launcher.ExpiresAt = &expires
	authorityPersist(t, s, launcher)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller' WHERE team='a'`)
	request := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Team: "b", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}, Dispatch: runspec.DispatchController}}, assertedProvenance(t, "ci", "", ""))
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('b','run-b',1,'ci','running',?,'request',1,'gh:1','https://github.com/acme/repo','controller','execute',?,'account',2,'r30',30)`, request, []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT 'b','run-b','job','pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='job'`)
	demand := UnitDemand{CapabilityPrefix: launcher.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
	return s, demand
}

func TestLauncherClaimProbesBoundedWorkPerTeamAndRotates(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	var execution []byte
	if err := s.db.QueryRow(`SELECT execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&execution); err != nil {
		t.Fatal(err)
	}
	for i := range launcherProbePerTeam {
		id := fmt.Sprintf("a%02d", i)
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(`{"id":"`+id+`"}`), []byte(`{"requires":{"set":true,"value":["unavailable"]}}`))
	}
	first, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || first.Team != "b" {
		t.Fatalf("backlogged team delayed another team's claim: claim=%+v err=%v", first, err)
	}
	second, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || second.Team != "a" || second.NodeID != "job" {
		t.Fatalf("bounded probe did not advance past ineligible work: claim=%+v err=%v", second, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE team='a' AND node_id='a00'`, execution)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT 'b','run-b','job2','pending',1,spec_json,execution_json FROM nodes WHERE team='b' AND node_id='job'`)
	demand.JobNamespace = "cloud"
	demand.ExcludedTeamLabels = []string{admission.TeamNodeValue("cloud", "b")}
	third, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || third.Team != "a" || third.NodeID != "a00" {
		t.Fatalf("bounded probe did not wrap to ready work: claim=%+v err=%v", third, err)
	}
	for _, claim := range []UnitClaim{second, third} {
		if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL WHERE team='a' AND node_id=?`, claim.NodeID)
	}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE team='a' AND node_id='a01'`, execution)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT 'a','run','z00','pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='job'`)
	fourth, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || fourth.NodeID != "a01" {
		t.Fatalf("new arrivals bypassed wrapped contender: node=%s err=%v", fourth.NodeID, err)
	}
}

func TestLauncherFinalRefusalKeepsRoundRobinProgress(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	var capabilities int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&capabilities); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_launcher_award BEFORE UPDATE ON nodes WHEN NEW.claimed_by IS NOT NULL BEGIN SELECT RAISE(ABORT,'award persistence unavailable'); END`)
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil || !strings.Contains(err.Error(), "award persistence unavailable") {
		t.Fatalf("final claim did not fail after selection: %v", err)
	}
	for _, table := range []string{"billing_awards", "billable_intervals", "node_attempts"} {
		rewriteSchemaCount(t, s.db, table, 0)
	}
	rewriteSchemaCount(t, s.db, "capabilities", capabilities)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_launcher_award`)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.Team != "b" {
		t.Fatalf("failed final claim starved another team: claim=%+v err=%v", claim, err)
	}
}

func TestLauncherUnpricedBacklogDoesNotDelayAnotherTeam(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	table := CreditRateTable{{Cores: 2, MicroPerSecond: 2}}
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE team='b'`, []byte(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":2}}}}`))
	for i := range launcherProbePerTeam {
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,'pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='job'`, fmt.Sprintf("unpriced-%03d", i))
	}
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrUnpricedCPUClass) {
		t.Fatalf("unpriced work was not refused: %v", err)
	}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.Team != "b" {
		t.Fatalf("unpriced backlog starved another team: claim=%+v err=%v", claim, err)
	}
}

func TestLauncherClaimsDrainEligiblePageBeforeNewArrivals(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	demand.JobNamespace = "cloud"
	demand.ExcludedTeamLabels = []string{admission.TeamNodeValue("cloud", "b")}
	for i := range 2 * launcherProbePerTeam {
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,'pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='job'`, fmt.Sprintf("a%02d", i))
	}
	for i := range 2 * launcherProbePerTeam {
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil || claim.NodeID != fmt.Sprintf("a%02d", i) {
			t.Fatalf("eligible contender %d bypassed: kind=%s run=%s node=%s err=%v", i, claim.Kind, claim.RunID, claim.NodeID, err)
		}
		if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL WHERE team='a' AND node_id=?`, claim.NodeID)
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,'pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='job'`, fmt.Sprintf("z%02d", i))
	}
}

func TestLauncherMixedKindsKeepUnawardedEligibleContenders(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	demand.JobNamespace = "cloud"
	demand.ExcludedTeamLabels = []string{admission.TeamNodeValue("cloud", "b")}
	demand.Kinds = []capability.UnitKind{capability.UnitNode, capability.UnitPlan}
	for i := range 2 * launcherProbePerTeam {
		id := fmt.Sprintf("a%02d", i)
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,'pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='job'`, id)
		rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,?,?,'ci','running',request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`, id, i+2)
		rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,?,'plan',available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE team='a' AND id='run'`, id)
	}
	first, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || first.Kind != capability.UnitNode || first.NodeID != "a00" {
		t.Fatalf("initial mixed-kind award: claim=%+v err=%v", first, err)
	}
	if err := s.ReleaseUnit(t.Context(), first, first.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	demand.Kinds = []capability.UnitKind{capability.UnitPlan}
	for i := range 2 * launcherProbePerTeam {
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil || claim.RunID != fmt.Sprintf("a%02d", i) {
			t.Fatalf("unawarded plan %d bypassed: kind=%s run=%s node=%s err=%v", i, claim.Kind, claim.RunID, claim.NodeID, err)
		}
		if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET available_at=? WHERE team='a' AND id=?`, s.now().Add(time.Hour).UnixNano(), claim.RunID)
	}
}
