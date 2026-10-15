package store

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
	"time"
)

func TestStoreDataCreditReadsStayInTenantAndUseStoreClock(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1000000, 0).UTC()
	s.clock = func() time.Time { return now }
	path := blobAccountingDiskStore(t, s)
	iteration := 0
	property := func(suffix string, amount, seconds uint16, reopen bool) bool {
		iteration++
		teams := []Team{NormalizeTeam(Team(fmt.Sprintf("reader_%d_%s_%%'", iteration, suffix))), NormalizeTeam(Team(fmt.Sprintf("reader_%d_%s_%%'_other", iteration, suffix)))}
		window := (time.Duration(seconds) + 1) * time.Second
		for i, team := range teams {
			rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", team)
			for j, kind := range []string{CreditGrantFree, CreditGrantPaid, CreditGrantReversal} {
				value := int64(amount) + int64(i+1)*100 + int64(j)
				if kind == CreditGrantReversal {
					value = -value
				}
				rewriteSchemaExec(t, s.db, "INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES (?,?,?,?,?)", fmt.Sprintf("%d_%d_%d", iteration, i, j), team, kind, value, j+1)
			}
			for j, kind := range []string{CreditChargeUsage, CreditChargeStorage, CreditChargeRefund} {
				at := now.Add(-window)
				value := int64(i+1)*10 + int64(j)
				if kind == CreditChargeUsage {
					at = at.Add(-time.Nanosecond)
				}
				if kind == CreditChargeRefund {
					at, value = now, -value
				}
				rewriteSchemaExec(t, s.db, "INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES (?,?, 'run','job','award',?,0,?,?)", fmt.Sprintf("charge_%d_%d_%d", iteration, i, j), team, kind, value, at.UnixNano())
			}
		}
		if reopen {
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = blobAccountingDB(t, path)
		}
		for i, team := range teams {
			tenant, err := s.ForTeam(t.Context(), team)
			if err != nil {
				t.Fatal(err)
			}
			state, err := tenant.CreditState(t.Context(), window)
			if err != nil {
				t.Fatal(err)
			}
			base, charge := int64(amount)+int64(i+1)*100, int64(i+1)*10
			if state.GrantedMicro != 2*base+1 || state.ReversedMicro != base+2 || state.ChargedMicro != charge-1 || state.StorageChargedMicro != charge+1 || state.BalanceMicro != base-charge || state.BurnMicro != -1 || state.BurnWindow != window {
				t.Logf("team=%q amount=%d window=%s: %+v", team, amount, window, state)
				return false
			}
			grants, err := tenant.ListCreditGrants(t.Context(), 10)
			if err != nil || len(grants) != 3 {
				t.Fatalf("team=%q history=%+v: %v", team, grants, err)
			}
			for j, grant := range grants {
				if grant.ID != fmt.Sprintf("%d_%d_%d", iteration, i, 2-j) {
					t.Logf("team=%q foreign or unordered grant=%+v", team, grant)
					return false
				}
			}
			lifetime, err := tenant.CreditState(t.Context(), 0)
			if err != nil || lifetime.BurnMicro != 0 || lifetime.BalanceMicro != state.BalanceMicro {
				t.Fatalf("team=%q lifetime state=%+v: %v", team, lifetime, err)
			}
		}
		now = now.Add(window + time.Nanosecond)
		for _, team := range teams {
			tenant, err := s.ForTeam(t.Context(), team)
			if err != nil {
				t.Fatal(err)
			}
			state, err := tenant.CreditState(t.Context(), window)
			if err != nil || state.BurnMicro != 0 {
				t.Fatalf("team=%q expired burn=%d: %v", team, state.BurnMicro, err)
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(5832))}); err != nil {
		t.Fatal(err)
	}
}
