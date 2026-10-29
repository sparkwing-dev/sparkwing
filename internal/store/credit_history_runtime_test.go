package store

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestStoreDataCreditHistoryPreservesTenantAndAwardIdentity(t *testing.T) {
	s := authorityStore(t)
	path := blobAccountingDiskStore(t, s)
	iteration := 0
	property := func(suffix string, amount uint16, reopen bool) bool {
		iteration++
		for owner := range 2 {
			team := NormalizeTeam(Team(fmt.Sprintf("history_%d_%d_%s_%%'--", iteration, owner, suffix)))
			rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", team)
			var wantGrants []CreditGrant
			var wantCharges []CreditCharge
			for row, kind := range []string{creditChargeReservation, creditChargeUsage, creditChargeRefund, CreditChargeStorage} {
				id := fmt.Sprintf("%04d_%d_%d", iteration, owner, row)
				at := time.Unix(0, int64(row/2+1)).UTC()
				charge := CreditCharge{ID: id, Team: team, RunID: "retained-run", NodeID: "job", TokenPrefix: "award_" + id, Principal: string(team), Kind: kind, Seconds: int64(row + 1), AmountMicro: int64(amount) + int64(row+1), CPUClassCores: 4, RateMicroPerSecond: 17, ChargedAt: at}
				if kind == creditChargeRefund {
					charge.AmountMicro, charge.Seconds = -charge.AmountMicro, -charge.Seconds
				}
				if kind == CreditChargeStorage {
					charge.RunID, charge.NodeID, charge.TokenPrefix, charge.Seconds, charge.StorageBytes = "", "", "", 0, int64(amount)
				}
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,principal,kind,seconds,amount_micro,storage_bytes,cpu_class,rate_micro_per_second,charged_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, charge.ID, charge.Team, charge.RunID, charge.NodeID, charge.TokenPrefix, charge.Principal, charge.Kind, charge.Seconds, charge.AmountMicro, charge.StorageBytes, charge.CPUClassCores, charge.RateMicroPerSecond, at.UnixNano())
				wantCharges = append(wantCharges, charge)
				if row == 3 {
					continue
				}
				grant := CreditGrant{ID: "grant_" + id, Team: team, Kind: []string{CreditGrantFree, CreditGrantPaid, creditGrantReversal}[row], AmountMicro: charge.AmountMicro, Reference: "ref_" + id, CreatedBy: "console", CreatedAt: at, ReversesKind: "paid"}
				if grant.Kind == creditGrantReversal {
					grant.Reverses = wantGrants[1].Reference
				}
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,reference,reverses,created_by,created_at) VALUES (?,?,?,?,?,?,?,?)`, grant.ID, grant.Team, grant.Kind, grant.AmountMicro, grant.Reference, grant.Reverses, grant.CreatedBy, at.UnixNano())
				wantGrants = append(wantGrants, grant)
			}
			if reopen {
				if err := s.db.Close(); err != nil {
					t.Fatal(err)
				}
				s.db = blobAccountingDB(t, path)
			}
			tenant, err := s.ForTeam(t.Context(), team)
			if err != nil {
				t.Fatal(err)
			}
			slices.Reverse(wantGrants)
			slices.Reverse(wantCharges)
			for _, limit := range []int{1, 2, 10, 0, -1, CreditHistoryMaxLimit + 1} {
				grants, err := tenant.ListCreditGrants(t.Context(), limit)
				if err != nil {
					t.Fatal(err)
				}
				charges, err := tenant.ListCreditCharges(t.Context(), limit)
				if err != nil {
					t.Fatal(err)
				}
				count := 10
				if limit > 0 {
					count = limit
				}
				if !reflect.DeepEqual(grants, wantGrants[:min(count, len(wantGrants))]) || !reflect.DeepEqual(charges, wantCharges[:min(count, len(wantCharges))]) {
					t.Logf("team=%q limit=%d: grants=%+v charges=%+v", team, limit, grants, charges)
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(52198))})); err != nil {
		t.Fatal(err)
	}
}

func TestCreditHistoryLimitIsClampedToItsMaximum(t *testing.T) {
	s := authorityStore(t)
	const count = CreditHistoryMaxLimit + 5
	rewriteSchemaExec(t, s.db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
	 INSERT INTO credit_grants(id,team,kind,amount_micro,created_at)
	 SELECT printf('%05d',i),'a','free',i,i FROM n`, count)
	rewriteSchemaExec(t, s.db, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
	 INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at)
	 SELECT printf('%05d',i),'a','retained','job','award','usage',1,i,i FROM n`, count)
	tenant, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ limit, want int }{{0, 200}, {-1, 200}, {1, 1}, {CreditHistoryMaxLimit, CreditHistoryMaxLimit}, {CreditHistoryMaxLimit + 5000, CreditHistoryMaxLimit}} {
		grants, err := tenant.ListCreditGrants(t.Context(), test.limit)
		if err != nil {
			t.Fatal(err)
		}
		charges, err := tenant.ListCreditCharges(t.Context(), test.limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(grants) != test.want || len(charges) != test.want {
			t.Fatalf("limit=%d grants=%d charges=%d, want %d", test.limit, len(grants), len(charges), test.want)
		}
		for _, i := range []int{0, test.want - 1} {
			want := fmt.Sprintf("%05d", count-i)
			if grants[i].ID != want || charges[i].ID != want || grants[i].Team != "a" || charges[i].Team != "a" {
				t.Fatalf("limit=%d index=%d: grant=%+v charge=%+v", test.limit, i, grants[i], charges[i])
			}
		}
	}
}
