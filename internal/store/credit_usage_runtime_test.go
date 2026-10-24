package store

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"testing/quick"
	"time"
)

func TestStoreDataCreditUsageStaysInTenantWithoutOperationalRows(t *testing.T) {
	s := authorityStore(t)
	path := blobAccountingDiskStore(t, s)
	iteration := 0
	property := func(suffix string, amount uint16, reopen bool) bool {
		iteration++
		for owner := range 2 {
			team := NormalizeTeam(Team(fmt.Sprintf("usage_%d_%d_%s_%%'--", iteration, owner, suffix)))
			rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", team)
			funding := int64(amount) + 1_000_000
			rewriteSchemaExec(t, s.db, "INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES (?,?,'free',?,1)", string(team), team, funding)
			var want []RunCreditUsage
			balance := funding
			for row, run := range []string{"run_%'--", "run_雪", "run"} {
				at := time.Unix(0, int64(row/2+1)).UTC()
				usage := RunCreditUsage{RunID: run, LastChargedAt: at}
				for k, kind := range []string{creditChargeReservation, creditChargeUsage, creditChargeRefund} {
					seconds := int64((owner + 1) * (row + 1) * (k + 1))
					value := int64(amount) + seconds
					if kind == creditChargeRefund {
						seconds, value = -seconds/2, -value/2
					}
					id := fmt.Sprintf("%d_%d_%d_%d", iteration, owner, row, k)
					rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES (?,?,?,'job',?,?,?,?,?)`, id, team, run, "award_"+run, kind, seconds, value, at.UnixNano())
					usage.Seconds += seconds
					usage.AmountMicro += value
					balance -= value
				}
				want = append(want, usage)
			}
			for row, kind := range []string{CreditChargeStorage, creditChargeUsage} {
				run := "run"
				if kind == creditChargeUsage {
					run = ""
				}
				id := fmt.Sprintf("excluded_%d_%d_%d", iteration, owner, row)
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES (?,?,?,'','',?,999,11,999)`, id, team, run, kind)
				balance -= 11
			}
			if reopen {
				if err := s.db.Close(); err != nil {
					t.Fatal(err)
				}
				s.db = blobAccountingDB(t, path)
			}
			slices.Reverse(want)
			tenant, err := s.ForTeam(t.Context(), team)
			if err != nil {
				t.Fatal(err)
			}
			gotBalance, err := tenant.CreditBalanceMicro(t.Context())
			if err != nil || gotBalance != balance {
				t.Fatalf("team=%q balance=%d, want %d: %v", team, gotBalance, balance, err)
			}
			for _, limit := range []int{0, -1, 1, 2, CreditHistoryMaxLimit + 1} {
				got, err := tenant.CreditUsageByRun(t.Context(), limit)
				if err != nil {
					t.Fatal(err)
				}
				count := len(want)
				if limit > 0 {
					count = min(count, limit)
				}
				if !slices.Equal(got, want[:count]) {
					t.Logf("team=%q limit=%d: usage=%+v, want %+v", team, limit, got, want[:count])
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(63012))}); err != nil {
		t.Fatal(err)
	}
}
