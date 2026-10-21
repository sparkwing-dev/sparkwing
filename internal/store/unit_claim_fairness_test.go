package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
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

func TestLauncherMatchingDemandsDoNotShareScanProgress(t *testing.T) {
	for _, observation := range []string{"labels", "availability"} {
		t.Run(observation, func(t *testing.T) {
			s, demand := launcherFairnessFixture(t)
			if observation == "availability" {
				demand.Capacity.MemoryBytes = 8
			}
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL`)
			for i := range 16 {
				label := "A"
				if i >= 8 {
					label = "B"
				}
				execution := []byte(fmt.Sprintf(`{"requires":{"set":true,"value":[%q]}}`, label))
				if observation == "availability" {
					cores, memory := 4, 1
					if label == "B" {
						cores, memory = 1, 4
					}
					execution = []byte(fmt.Sprintf(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":%d},"memory_bytes":{"set":true,"value":%d}}}}`, cores, memory))
				}
				rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, fmt.Sprintf("job-%02d", i), []byte(`{}`), execution)
			}
			counts := map[string]int{}
			for round := range 16 {
				for _, label := range []string{"B", "A"} {
					demand.Labels = []string{label}
					if observation == "availability" {
						demand.Labels = nil
						demand.Available = match.Resources{Cores: 8, MemoryBytes: 1}
						if label == "B" {
							demand.Available = match.Resources{Cores: 1, MemoryBytes: 8}
						}
					}
					claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
					if errors.Is(err, ErrNotFound) {
						continue
					}
					if err != nil {
						t.Fatalf("round %d claimant %s: %v", round, label, err)
					}
					first := 0
					if label == "B" {
						first = 8
					}
					if claim.NodeID != fmt.Sprintf("job-%02d", first+counts[label]) {
						t.Fatalf("claimant %s skipped its oldest work: %s", label, claim.NodeID)
					}
					counts[label]++
					if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
						t.Fatal(err)
					}
					rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL WHERE team=? AND run_id=? AND node_id=?`, claim.Team, claim.RunID, claim.NodeID)
				}
			}
			if counts["A"] != 8 || counts["B"] != 8 {
				t.Fatalf("matching demands starved each other: %v", counts)
			}
		})
	}
}

func TestLauncherSeededMatchingDemandFairness(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL`)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET available_at=?`, s.now().Add(time.Hour).UnixNano())
	var request []byte
	if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&request); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(request, &sealed); err != nil {
		t.Fatal(err)
	}
	type waiting struct {
		label string
		born  int
	}
	pending := map[UnitRef]waiting{}
	serial := 0
	add := func(label string, kind capability.UnitKind, born int) {
		serial++
		id := fmt.Sprintf("sim-%05d", serial)
		unit := UnitRef{Kind: kind, RunID: "run", NodeID: id}
		if kind == capability.UnitNode {
			execution := []byte(fmt.Sprintf(`{"requires":{"set":true,"value":[%q]}}`, label))
			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(`{}`), execution)
		} else {
			unit.RunID, unit.NodeID = id, ""
			seal := sealed
			seal.Request.Options.Requires.Value = []string{label}
			seal.Request.Options.Requires.Set = true
			body := sealWith(t, seal, sealed.Provenance)
			rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,?,?,'ci','running',?,'request',request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,?,log_retention_class,log_retention_days FROM runs WHERE id='run'`, id, serial+10, body, serial+2)
			rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('a',?,?,1,1,?,?,?)`, id, kind, s.now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
		}
		pending[unit] = waiting{label: label, born: born}
	}
	for _, label := range []string{"A", "B"} {
		for range 8 {
			add(label, capability.UnitNode, 0)
		}
	}
	for range 4 {
		add("C", capability.UnitPlan, 0)
		add("C", capability.UnitPlan, 0)
	}
	rng := rand.New(rand.NewPCG(47, 113))
	labels := []string{"B", "A", "C"}
	for turn := range 240 {
		// Arrivals continue through each sweep, slower than aggregate claimant capacity.
		if turn < 150 && turn%3 != 0 {
			label := labels[rng.IntN(len(labels))]
			kind := capability.UnitNode
			if label == "C" {
				kind = capability.UnitPlan
			}
			add(label, kind, turn)
		}
		label := labels[turn%len(labels)]
		demand.Labels = []string{label}
		demand.Kinds = []capability.UnitKind{capability.UnitNode}
		if label == "C" {
			demand.Kinds = []capability.UnitKind{capability.UnitPlan}
		}
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if err == nil {
			unit := UnitRef{Kind: claim.Kind, RunID: claim.RunID, NodeID: claim.NodeID, SourceGeneration: claim.SourceGeneration}
			job, ok := pending[unit]
			if !ok || job.label != label {
				t.Fatalf("claimant %s claimed ineligible work %+v (%+v)", label, unit, job)
			}
			delete(pending, unit)
			if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			if claim.Kind == capability.UnitNode {
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL WHERE run_id=? AND node_id=?`, claim.RunID, claim.NodeID)
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET available_at=? WHERE id=?`, s.now().Add(time.Hour).UnixNano(), claim.RunID)
			}
		}
		for unit, job := range pending {
			if turn-job.born > 96 {
				t.Fatalf("eligible %s work %+v starved for %d polls", job.label, unit, turn-job.born)
			}
		}
	}
	if len(pending) != 0 {
		t.Fatalf("%d eligible jobs remained after arrivals stopped", len(pending))
	}
}

func TestLauncherSweepRetentionFollowsClaimantLifetime(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["missing"]}}`))
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected sweep: %v", err)
	}
	var key string
	if err := s.db.QueryRow(`SELECT key FROM settings WHERE key LIKE 'launcher_sweep/%' LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('launcher_sweep/live',?)`, []byte(`{"claimant":"runner"}`))
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET expires_at=? WHERE prefix=?`, s.now().Add(-8*24*time.Hour).UnixNano(), demand.CapabilityPrefix)
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Lease: lease, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key=?`, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan sweep retained: %d %v", count, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='launcher_sweep/live'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("live sweep removed: %d %v", count, err)
	}
}

func TestLauncherSweepFrontierExcludesLaterArrivalsInOldRun(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL`)
	for i := range launcherProbePerTeam {
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, fmt.Sprintf("a%03d", i), []byte(`{}`), []byte(`{"requires":{"set":true,"value":["missing"]}}`))
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','z-target','pending',2,?,?)`, []byte(`{}`), []byte(`{}`))
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("initial bounded sweep: %v", err)
	}
	for poll := range 4 {
		for i := range launcherProbePerTeam {
			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',?, ?,?)`, fmt.Sprintf("b%03d-%03d", poll, i), poll+3, []byte(`{}`), []byte(`{"requires":{"set":true,"value":["missing"]}}`))
		}
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if claim.NodeID != "z-target" {
			t.Fatalf("later arrival bypassed target: %+v", claim)
		}
		return
	}
	t.Fatal("continuous arrivals extended the sweep past its frontier")
}
