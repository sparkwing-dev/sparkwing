package store

import (
	"errors"
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestMeteredUnpricedClaimFailsWorkWithoutBilling(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		for _, queued := range []bool{false, true} {
			s, demand, _ := meteredUnitFixture(t, cloud)
			table := CreditRateTable{{Cores: 1, MicroPerSecond: 2}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
				t.Fatal(err)
			}
			if queued {
				demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
			}
			_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			var unpriced *UnpricedCPUClassError
			if !errors.As(err, &unpriced) || unpriced.Cores != 4 || unpriced.MaxCores != 1 {
				t.Fatalf("cloud=%t queued=%t: unpriced claim=%v", cloud, queued, err)
			}
			row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeFailed || row.ClaimedBy != "" || row.CapabilityPrefix != "" || row.Attempt != 0 || row.FinishedAt == nil {
				t.Fatalf("unpriced claim did not fail work: %+v: %v", row, err)
			}
			for _, name := range []string{"credit_charges", "billing_awards", "billable_intervals", "node_attempts"} {
				rewriteSchemaCount(t, s.db, name, 0)
			}
			if balance, err := (&Tenant{s: s, team: "a"}).CreditBalanceMicro(t.Context()); err != nil || balance != 1000000 {
				t.Fatalf("unpriced claim changed balance: %d: %v", balance, err)
			}
		}
	}
}

func TestUnpricedFailureRequiresCurrentClaimScopeProperty(t *testing.T) {
	property := func(seed uint32, variant uint8) bool {
		s, demand, _ := meteredUnitFixture(t, true)
		table := CreditRateTable{{Cores: 1, MicroPerSecond: 2}}
		if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
			t.Fatal(err)
		}
		tenant := &Tenant{s: s, team: "a"}
		actor := runstate.Actor{Kind: runstate.ActorClaimant, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, CapabilityPrefix: demand.CapabilityPrefix}
		change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeFailed, Cause: runstate.CauseUnpriced, Actor: actor, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: FailureUnpricedCPUClass}}}
		switch variant % 4 {
		case 0:
			change.Actor.Authority.ID = fmt.Sprintf("missing-%x", seed)
		case 1:
			other := authorityRoot(fmt.Sprintf("foreign-%x", seed), "b")
			other.RootMetered = true
			authorityPersist(t, s, other)
			change.Actor.Authority.ID, change.Actor.CapabilityPrefix = other.Prefix, other.Prefix
		case 2:
			table = CreditRateTable{{Cores: 8, MicroPerSecond: 2}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
				t.Fatal(err)
			}
		case 3:
			if err := s.inTx(t.Context(), func(tx *storeTx) error { return tx.operator.revokeAuthorityTx(t.Context(), tx, actor.Authority) }); err != nil {
				t.Fatal(err)
			}
		}
		_, err := tenant.TransitionNode(t.Context(), change)
		row, readErr := tenant.ReadNode(t.Context(), change.Ref)
		return err != nil && readErr == nil && row.Status == runstate.StatusPending && row.Outcome == "" && row.StateSeq == 0 && row.FinishedAt == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestUnpricedFailureKeepsThePollersMatchingEvidence(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, false)
	table := CreditRateTable{{Cores: 1, MicroPerSecond: 2}}
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["sparkwing-4cpu","tool:terraform"]}}`))
	demand.Labels = []string{"sparkwing-4cpu", "tool:terraform"}
	_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	row, readErr := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if !errors.Is(err, ErrUnpricedCPUClass) || readErr != nil || row.Outcome != runstate.NodeFailed {
		t.Fatalf("matching poller left unpriced work pending: %+v %v %v", row, err, readErr)
	}
}

func TestUnmeteredWorkDoesNotNeedAPricedClass(t *testing.T) {
	for _, queued := range []bool{false, true} {
		s, demand := unitClaimFixture(t)
		table := CreditRateTable{{Cores: 1, MicroPerSecond: 2}}
		if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
			t.Fatal(err)
		}
		if queued {
			demand.Target = nil
			demand.Kinds = []capability.UnitKind{capability.UnitNode}
		}
		if claim, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil || claim.Award.CapabilityPrefix == "" {
			t.Fatalf("unmetered work was priced: %+v %v", claim, err)
		}
	}
}
