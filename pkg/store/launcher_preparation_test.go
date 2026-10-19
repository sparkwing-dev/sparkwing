package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestLauncherPreparationKeepsBootstrapUntilExchange(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, now := launcherSnapshotFixture(t, kind)
			root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
			awards, err := s.ReadLauncherAwards(t.Context(), root, nil)
			if err != nil || len(awards) != 1 {
				t.Fatalf("initial award: %+v %v", awards, err)
			}
			fence := capability.LeaseRef{Kind: kind, ID: awards[0].Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
			previous := claim.Award.LeaseExpiresAt
			for range 4 {
				*now = now.Add(limits.DefaultLease * 2 / 3)
				if _, err = s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
					t.Fatal(err)
				}
				awards, err = s.ReadLauncherAwards(t.Context(), root, nil)
				if err != nil || len(awards) != 1 || !awards[0].Claim.Award.LeaseExpiresAt.After(previous) {
					t.Fatalf("preparation lost lease: %+v %v", awards, err)
				}
				previous = awards[0].Claim.Award.LeaseExpiresAt
				if len(awards[0].Capability.Grants) != 2 {
					t.Fatal("preparation broadened bootstrap authority")
				}
			}
			if _, err = s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Minute)
			if _, err = s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
				t.Fatal(err)
			}
			awards, err = s.ReadLauncherAwards(t.Context(), root, nil)
			if err != nil || len(awards) != 1 || !awards[0].Claim.Award.LeaseExpiresAt.Equal(previous) {
				t.Fatalf("launcher renewed exchanged authority: %+v %v", awards, err)
			}
			*now = previous
			prepared, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence})
			if err != nil || len(prepared) != 0 {
				t.Fatalf("expired award was retained: %+v %v", prepared, err)
			}
		})
	}
}

func FuzzLauncherPreparationKeepsExactUnexchangedFence(f *testing.F) {
	f.Add(uint8(0), uint16(1))
	f.Add(uint8(1), uint16(2))
	f.Add(uint8(2), uint16(3))
	f.Add(uint8(3), uint16(4))
	f.Fuzz(func(t *testing.T, change uint8, offset uint16) {
		s, demand, claim, now := launcherSnapshotFixture(t, capability.UnitNode)
		root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		awards, err := s.ReadLauncherAwards(t.Context(), root, nil)
		if err != nil || len(awards) != 1 {
			t.Fatal(err)
		}
		fence := capability.LeaseRef{Kind: capability.UnitNode, ID: awards[0].Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
		before := awards[0].Claim.Award.LeaseExpiresAt
		switch change % 4 {
		case 0:
			fence.Generation += int64(offset) + 1
		case 1:
			fence.Holder += "other"
		case 2:
			root.ID += "other"
		case 3:
			*now = before.Add(time.Duration(offset))
		}
		prepared, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence})
		if change%4 != 3 && err == nil {
			t.Fatal("foreign or forged original award accepted")
		}
		if len(prepared) != 0 {
			t.Fatal("foreign, stale or expired preparation retained")
		}
		var after int64
		if err = s.DB().QueryRowContext(t.Context(), `SELECT lease_expires_at FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before.UnixNano() {
			t.Fatal("rejected preparation changed persisted lease")
		}
	})
}

func TestLauncherPreparationSkipsEndedOwnAwards(t *testing.T) {
	for _, ended := range []string{"expired", "cancelled", "finished"} {
		t.Run(ended, func(t *testing.T) {
			s, demand, first, now := launcherSnapshotFixture(t, capability.UnitNode)
			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','other','pending',1,?,?)`, []byte(`{"id":"other"}`), []byte(`{}`))
			demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "other"}
			second, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
			fences := []capability.LeaseRef{}
			for _, claim := range []UnitClaim{first, second} {
				p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				fences = append(fences, capability.LeaseRef{Kind: capability.UnitNode, ID: p.Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation})
			}
			*now = now.Add(time.Minute)
			switch ended {
			case "expired":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=? WHERE node_id='job'`, now.UnixNano())
			case "cancelled":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET cancel_requested_at=1 WHERE node_id='job'`)
			case "finished":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE node_id='job'`)
			}
			prepared, err := s.RenewLauncherPreparation(t.Context(), root, fences)
			if err != nil || len(prepared) != 1 || prepared[0] != fences[1] {
				t.Fatalf("ended award blocked live preparation: %+v %v", prepared, err)
			}
		})
	}
}

func FuzzLauncherPreparationBillingAndGrace(f *testing.F) {
	f.Add(uint8(0), uint8(11))
	f.Add(uint8(1), uint8(23))
	f.Add(uint8(2), uint8(47))
	f.Fuzz(func(t *testing.T, selector, step uint8) {
		kind := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[selector%3]
		s, demand, claim, now := launcherSnapshotFixture(t, kind)
		root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		awards, err := s.ReadLauncherAwards(t.Context(), root, nil)
		if err != nil || len(awards) != 1 {
			t.Fatal(err)
		}
		fence := capability.LeaseRef{Kind: kind, ID: awards[0].Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
		if _, err = s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
			t.Fatal(err)
		}
		total := func() int64 {
			t.Helper()
			var amount int64
			if err := s.DB().QueryRowContext(t.Context(), `SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges`).Scan(&amount); err != nil {
				t.Fatal(err)
			}
			return amount
		}
		before := total()
		*now = now.Add(time.Minute + time.Duration(step%60)*time.Second)
		if prepared, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil || len(prepared) != 1 {
			t.Fatalf("live preparation: %+v %v", prepared, err)
		}
		if total() != before {
			t.Fatal("Cloud preparation charged before the pod started")
		}
		rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
		*now = now.Add(time.Duration(15+step%45) * time.Second)
		if prepared, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil || len(prepared) != 0 {
			t.Fatalf("an unpaid balance kept preparing: %+v %v; want a hard stop at the next renewal", prepared, err)
		}
		awards, err = s.ReadLauncherAwards(t.Context(), root, nil)
		if err != nil || len(awards) != 0 {
			t.Fatalf("financially cancelled preparation remained exchangeable: %+v %v", awards, err)
		}
		if _, err = s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err == nil {
			t.Fatal("cancelled preparation exchanged authority")
		}
	})
}
