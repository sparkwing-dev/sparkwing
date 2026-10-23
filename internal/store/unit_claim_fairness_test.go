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
	indexFixtureUnit(t, s, "b", UnitRef{Kind: capability.UnitNode, RunID: "run-b", NodeID: "job"})
	demand := UnitDemand{CapabilityPrefix: launcher.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}}
	return s, demand
}

func TestClaimSeededDisjointLabelsAndFairShare(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
	rng := rand.New(rand.NewPCG(44, 3095))
	labels := []string{"arm", "gpu", "ordinary"}
	pending := map[string]string{}
	sequence := 0
	arrive := func(label string) {
		t.Helper()
		for _, team := range []string{"a", "b"} {
			run := "run"
			if team == "b" {
				run = "run-b"
			}
			node := fmt.Sprintf("job-%06d", sequence)
			sequence++
			execution, _ := json.Marshal(map[string]any{"requires": map[string]any{"value": []string{label}}})
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				requirements, err := persistClaimRequirementsTx(t.Context(), tx, Team(team), []string{label})
				if err != nil {
					return err
				}
				claimContext, err := persistClaimContextTx(t.Context(), tx, Team(team), run, capability.UnitNode, requirements, execution)
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) VALUES (?,?,?,'pending',?,?,?,?,?)`, team, run, node, sequence, []byte(`{}`), execution, requirements, claimContext)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			pending[team+"/"+node] = label
		}
	}
	for _, label := range labels {
		for range 6 {
			arrive(label)
		}
	}
	claimants := map[string]string{}
	for _, label := range labels {
		claimant := authorityRoot("claimant-"+label, "")
		claimant.Subject.Kind = capability.SubjectService
		claimant.Grants = []capability.Grant{capability.ClaimsLaunch}
		expires := s.now().Add(time.Hour)
		claimant.ExpiresAt = &expires
		authorityPersist(t, s, claimant)
		claimants[label] = claimant.Prefix
	}
	awards := map[string]int{}
	last := map[string]int{}
	ordinal := 0
	claim := func(label string, inject bool) bool {
		t.Helper()
		demand.Labels = []string{label}
		demand.CapabilityPrefix = claimants[label]
		expected := ""
		for key, required := range pending {
			if required != label {
				continue
			}
			team, node, _ := strings.Cut(key, "/")
			if expected == "" {
				expected = key
				continue
			}
			previousTeam, previousNode, _ := strings.Cut(expected, "/")
			if last[team] < last[previousTeam] || last[team] == last[previousTeam] && node < previousNode {
				expected = key
			}
		}
		if inject {
			fault := `CREATE TRIGGER claim_fault BEFORE UPDATE ON nodes WHEN NEW.claimed_by IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected claim fault'); END`
			if ordinal%2 == 1 {
				fault = `CREATE TRIGGER claim_fault BEFORE UPDATE OF claim_award_ordinal ON teams BEGIN SELECT RAISE(ABORT,'injected claim fault after ordinal'); END`
			}
			rewriteSchemaExec(t, s.db, fault)
			_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			rewriteSchemaExec(t, s.db, `DROP TRIGGER claim_fault`)
			if err == nil {
				t.Fatal("injected award unexpectedly committed")
			}
			var after int
			if err := s.db.QueryRow(`SELECT ordinal FROM claim_awards WHERE singleton=1`).Scan(&after); err != nil || after != ordinal {
				t.Fatalf("failed award advanced fair share: before=%d after=%d error=%v", ordinal, after, err)
			}
		}
		result, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if errors.Is(err, ErrNotFound) {
			if expected != "" {
				t.Fatalf("eligible prefix hidden for claimant %s: expected %s", label, expected)
			}
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		key := string(result.Team) + "/" + result.NodeID
		if key != expected {
			t.Fatalf("fair share/FIFO award=%s expected=%s ordinals=%v", key, expected, last)
		}
		ordinal++
		last[string(result.Team)] = ordinal
		if required, ok := pending[key]; !ok || required != label {
			t.Fatalf("ineligible or duplicate award %s to %s (requires %s)", key, label, required)
		}
		if err := s.ReleaseUnit(t.Context(), result, result.Award.CapabilityPrefix); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=NULL,status='done',outcome='success',finished_at=1 WHERE team=? AND run_id=? AND node_id=?`, result.Team, result.RunID, result.NodeID)
		delete(pending, key)
		awards[string(result.Team)]++

		return true
	}
	claim("ordinary", true)
	for tick := range 90 {
		if tick%3 == 0 {
			arrive(labels[rng.IntN(len(labels))])
		}
		claim(labels[rng.IntN(len(labels))], tick%17 == 0)
	}
	for turns := 0; len(pending) > 0 && turns < sequence*3; turns++ {
		claim(labels[turns%len(labels)], false)
	}
	if len(pending) > 0 {
		t.Fatalf("eligible jobs stranded: %v", pending)
	}
	if awards["a"] != awards["b"] {
		t.Fatalf("paired arrivals drained unfairly: %v", awards)
	}
	var scans int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'launcher_sweep/%' OR key='launcher_claim_team'`).Scan(&scans); err != nil || scans != 0 {
		t.Fatalf("persisted scan state=%d err=%v", scans, err)
	}
}

func TestClaimExcludedTeamAppliesToNamedAndPoolClaims(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			s, demand := launcherFairnessFixture(t)
			demand.JobNamespace = "jobs"
			demand.ExcludedTeamLabels = []string{admission.TeamNodeValue("jobs", "a"), admission.TeamNodeValue("jobs", "b")}
			if named {
				demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
			}
			if claim, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil {
				t.Fatalf("excluded award: %+v", claim)
			}
		})
	}
}
