package store_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestAccountAdmissionAuditRemainsOutsideTenantQueriesProperty(t *testing.T) {
	s := storetest.Open(t)
	random := rand.New(rand.NewPCG(781, 41))
	for i := range 16 {
		separator := []string{".", "+", "-", "_", "'", ""}[random.IntN(6)]
		name := fmt.Sprintf("audit%s%x", separator, random.Uint64())
		account := signIn(t, s, fmt.Sprintf("subject-%d", i), name+"@example.test")
		var count int
		if err := s.DB().QueryRowContext(t.Context(), storetest.Rebind(s, `SELECT COUNT(*) FROM business_events WHERE account=? AND kind=? AND team IS NULL`), account.Account.ID, store.BusinessEventAccountAdmitted).Scan(&count); err != nil || count != 1 {
			t.Fatalf("account admission audit: count=%d, %v", count, err)
		}
		rows, err := tenant(t, s, account.PersonalTeam).BusinessEvents(t.Context(), "")
		if err != nil || len(rows) != 1 || rows[0].Kind != store.BusinessEventTeamCreated || rows[0].Team != account.PersonalTeam {
			t.Fatalf("tenant audit: %+v, %v", rows, err)
		}
	}
}
