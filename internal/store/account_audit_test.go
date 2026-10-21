package store_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAccountAdmissionAuditRemainsOutsideTenantQueriesProperty(t *testing.T) {
	s := storetest.Open(t)
	random := rand.New(rand.NewPCG(781, 41))
	for i := range 16 {
		separator := []string{".", "+", "-", "_", "'", ""}[random.IntN(6)]
		name := fmt.Sprintf("audit%s%x", separator, random.Uint64())
		account := signIn(t, s, fmt.Sprintf("subject-%d", i), name+"@example.test")
		var count int
		if err := s.DB().QueryRowContext(t.Context(), storetest.Rebind(s, `SELECT COUNT(*) FROM audit_events WHERE account=? AND kind=? AND team IS NULL`), account.Account.ID, store.BusinessEventAccountAdmitted).Scan(&count); err != nil || count != 1 {
			t.Fatalf("account admission audit: count=%d, %v", count, err)
		}
		var actor string
		if err := s.DB().QueryRowContext(t.Context(), storetest.Rebind(s, `SELECT actor FROM audit_events WHERE account=? AND kind=?`), account.Account.ID, store.BusinessEventTeamCreated).Scan(&actor); err != nil || actor != account.Account.ID || strings.Contains(actor, "@") {
			t.Fatalf("team creation must name an account id: %q %v", actor, err)
		}
		rows, err := tenant(t, s, account.PersonalTeam).BusinessEvents(t.Context(), "", 1000, "")
		if err != nil || len(rows) != 1 || rows[0].Kind != store.BusinessEventTeamCreated || rows[0].Team != account.PersonalTeam {
			t.Fatalf("tenant audit: %+v, %v", rows, err)
		}
	}
}

func TestAuditReaderLabelsDeletedAccount(t *testing.T) {
	s := storetest.Open(t)
	account := signIn(t, s, "audit-deletion", "person@example.test")
	tn := tenant(t, s, account.PersonalTeam)
	before, err := tn.BusinessEvents(t.Context(), store.BusinessEventTeamCreated, 10, "")
	if err != nil || len(before) != 1 || before[0].Actor != account.Account.ID {
		t.Fatalf("live account: %+v %v", before, err)
	}
	if _, err := s.DeleteAccount(t.Context(), account.Account.ID); err != nil {
		t.Fatal(err)
	}
	after, err := tn.BusinessEvents(t.Context(), store.BusinessEventTeamCreated, 10, "")
	if err != nil || len(after) != 1 || after[0].Actor != "deleted account" || after[0].Account != "deleted account" {
		t.Fatalf("deleted account: %+v %v", after, err)
	}
	var actor string
	if err := s.DB().QueryRowContext(t.Context(), storetest.Rebind(s, `SELECT actor FROM audit_events WHERE id=?`), before[0].ID).Scan(&actor); err != nil || actor != account.Account.ID {
		t.Fatalf("deletion changed audit row: %q %v", actor, err)
	}
}
