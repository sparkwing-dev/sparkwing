package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLauncherRejectionFailsExactAward(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		for _, exchange := range []bool{false, true} {
			t.Run(string(kind)+map[bool]string{false: "/bootstrap", true: "/exchanged"}[exchange], func(t *testing.T) {
				s, demand, claim, _ := launcherSnapshotFixture(t, kind)
				root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
				awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil)
				if err != nil || len(awards) != 1 {
					t.Fatal(err)
				}
				fence := capability.LeaseRef{Kind: kind, ID: awards[0].Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
				if exchange {
					if _, err = s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err != nil {
						t.Fatal(err)
					}
				}
				if err = s.rejectLauncherAwards(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
					t.Fatal(err)
				}
				awards, _, err = s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil)
				if err != nil || len(awards) != 0 {
					t.Fatalf("rejected award retained: %+v %v", awards, err)
				}
				var status string
				switch kind {
				case capability.UnitNode:
					err = s.DB().QueryRow(`SELECT outcome FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&status)
				case capability.UnitPlan:
					err = s.DB().QueryRow(`SELECT status FROM runs WHERE id='run'`).Scan(&status)
				case capability.UnitEval:
					err = s.DB().QueryRow(`SELECT expansion FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&status)
				}
				if err != nil || status != "failed" {
					t.Fatalf("rejection outcome %s: %v", status, err)
				}
				if err = s.rejectLauncherAwards(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
					t.Fatalf("rejection replay: %v", err)
				}
			})
		}
	}
}

func FuzzLauncherRejectRequiresOwnExactAward(f *testing.F) {
	f.Add(uint8(0), uint16(1))
	f.Add(uint8(1), uint16(2))
	f.Add(uint8(2), uint16(3))
	f.Fuzz(func(t *testing.T, field uint8, change uint16) {
		s, demand, claim, _ := launcherSnapshotFixture(t, capability.UnitNode)
		root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil)
		if err != nil || len(awards) != 1 {
			t.Fatal(err)
		}
		fence := capability.LeaseRef{Kind: capability.UnitNode, ID: awards[0].Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
		switch field % 3 {
		case 0:
			fence.Generation += int64(change) + 1
		case 1:
			fence.Holder += "foreign"
		case 2:
			root.ID += "foreign"
		}
		if err = s.rejectLauncherAwards(t.Context(), root, []capability.LeaseRef{fence}); err == nil {
			t.Fatal("forged rejection accepted")
		}
		after, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, nil, nil, nil, nil)
		if err != nil || len(after) != 1 || after[0].Claim.Award != awards[0].Claim.Award {
			t.Fatalf("foreign rejection changed award: %+v %v", after, err)
		}
	})
}

func TestLauncherRejectionPreservesReplacementAward(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, first, _ := launcherSnapshotFixture(t, kind)
			root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
			awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil)
			if err != nil || len(awards) != 1 {
				t.Fatal(err)
			}
			fence := capability.LeaseRef{Kind: kind, ID: awards[0].Capability.UnitID, Holder: first.Award.Holder, Generation: first.Award.Generation}
			if kind == capability.UnitNode {
				exchanged, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: first.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				first.Award.CapabilityPrefix = exchanged.Capability.Prefix
			}
			if err = s.ReleaseUnit(t.Context(), first, first.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitNode {
				rewriteSchemaExec(t, s.DB(), `UPDATE nodes SET ready_at=1 WHERE run_id='run' AND node_id='job'`)
			}
			next, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.rejectLauncherAwards(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
				t.Fatal(err)
			}
			remaining, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil)
			if err != nil || len(remaining) != 1 || remaining[0].Claim.Award != next.Award {
				t.Fatalf("stale rejection damaged replacement: %+v %v", remaining, err)
			}
		})
	}
}
