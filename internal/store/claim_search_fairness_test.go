package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestClaimRecheckRefusesStaleFairShare(t *testing.T) {
	s, first := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE team='a'`, []byte(`{"requires":{"value":["sparkwing-4cpu","east"]}}`))
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=3,execution_json=? WHERE team='b'`, []byte(`{"requires":{"value":["sparkwing-4cpu","west"]}}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','second','pending',2,?,?)`, []byte(`{"id":"second"}`), []byte(`{"requires":{"value":["sparkwing-4cpu","west"]}}`))
	for _, unit := range []UnitRef{{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, {Kind: capability.UnitNode, RunID: "run", NodeID: "second"}, {Kind: capability.UnitNode, RunID: "run-b", NodeID: "job"}} {
		team := Team("a")
		if unit.RunID == "run-b" {
			team = "b"
		}
		indexFixtureUnit(t, s, team, unit)
	}
	first.Labels = []string{"east"}
	second := first
	second.Labels = []string{"west"}
	unit, err := s.AsOperator().searchClaimUnit(t.Context(), second)
	if err != nil || unit.Unit.RunID != "run" || unit.Unit.NodeID != "second" {
		t.Fatalf("initial candidate: %+v %v", unit, err)
	}
	if claim, err := s.AsOperator().ClaimUnit(t.Context(), first); err != nil || claim.Team != "a" {
		t.Fatalf("first award: %+v %v", claim, err)
	}
	claim, err := recheckClaimCandidate(t, s, second, unit)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale team A candidate awarded after A advanced past eligible B: %+v %v", claim, err)
	}
	for _, team := range []Team{"b", "a"} {
		claim, err := s.AsOperator().ClaimUnit(t.Context(), second)
		if err != nil || claim.Team != team {
			t.Fatalf("next poll: %+v %v; want team %s", claim, err, team)
		}
	}
}

func TestClaimRecheckKeepsFairShareAfterOtherTeamAward(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	candidate, err := s.AsOperator().searchClaimUnit(t.Context(), demand)
	if err != nil || candidate.Team != "a" {
		t.Fatalf("initial candidate: %+v %v", candidate, err)
	}
	named := demand
	named.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run-b", NodeID: "job"}
	if claim, err := s.AsOperator().ClaimUnit(t.Context(), named); err != nil || claim.Team != "b" {
		t.Fatalf("other team award: %+v %v", claim, err)
	}
	if claim, err := recheckClaimCandidate(t, s, demand, candidate); err != nil || claim.Team != "a" {
		t.Fatalf("unchanged team lost its turn: %+v %v", claim, err)
	}
}

func recheckClaimCandidate(t *testing.T, s *Store, demand UnitDemand, candidate claimHead) (UnitClaim, error) {
	t.Helper()
	var claim UnitClaim
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		if err := lockClaimQueueTx(t.Context(), tx); err != nil {
			return err
		}
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		if err := tx.operator.lockAuthorityTx(t.Context(), tx, []capability.AuthorityRef{ref}); err != nil {
			return err
		}
		var err error
		claim, err = tx.operator.claimUnitTx(t.Context(), tx, demand, candidate)
		return err
	})
	return claim, err
}
