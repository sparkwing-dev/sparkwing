package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestLauncherClaimProbesBoundedWorkPerTeamAndRotates(t *testing.T) {
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

	var execution []byte
	if err := s.db.QueryRow(`SELECT execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&execution); err != nil {
		t.Fatal(err)
	}
	request := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Team: "b", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}, Dispatch: runspec.DispatchController}}, assertedProvenance(t, "ci", "", ""))
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('b','run-b',1,'ci','running',?,'request',1,'gh:1','https://github.com/acme/repo','controller','execute',?,'account',2,'r30',30)`, request, []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('b','run-b','job','pending',1,?,?)`, []byte(`{"id":"job"}`), execution)
	for i := range launcherProbePerTeam {
		id := fmt.Sprintf("a%02d", i)
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(`{"id":"`+id+`"}`), []byte(`{"requires":{"set":true,"value":["unavailable"]}}`))
	}
	demand := UnitDemand{CapabilityPrefix: launcher.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
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
}

type failSecondClaimProfile struct{ calls int }

func (p *failSecondClaimProfile) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	p.calls++
	if p.calls == 2 {
		return match.Profile{}, match.Resources{}, errors.New("transient claim profile failure")
	}
	return match.Profile{Capacity: match.Resources{Cores: 8}}, match.Resources{Cores: 8}, nil
}

func TestLauncherFinalRefusalKeepsRoundRobinProgress(t *testing.T) {
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
	profile := &failSecondClaimProfile{}
	s.claimProfiles = profile
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil || profile.calls != 2 {
		t.Fatalf("final claim did not fail after selection: calls=%d err=%v", profile.calls, err)
	}
	s.claimProfiles = nil
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.Team != "b" {
		t.Fatalf("failed final claim starved another team: claim=%+v err=%v", claim, err)
	}
}
