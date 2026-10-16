package store

import (
	"reflect"
	"testing"
)

func TestStoreRetiresTokenMeteredBilling(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"FinalizeNodeCredits", "TokenMetered", "CreateToken", "CreateTokenWith"} {
		if _, exists := reflect.TypeOf(s).MethodByName(name); exists {
			t.Errorf("retired API %s remains", name)
		}
	}
	for _, name := range []string{"CreateToken", "CreateTokenWith"} {
		if _, exists := reflect.TypeOf(&Tenant{s: s, team: DefaultTeam}).MethodByName(name); exists {
			t.Errorf("retired tenant API %s remains", name)
		}
	}
	for _, name := range []string{"OpenCreditCheckout", "AttachCreditCheckout", "DropCreditCheckout", "SetSpendBudget", "StartRecoveryAttempt"} {
		if _, exists := reflect.TypeOf(&Tenant{}).MethodByName(name); exists {
			t.Errorf("raw billing mutation %s remains", name)
		}
	}
	for _, table := range []string{"nodes", "triggers"} {
		rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var fallback any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &fallback, &pk); err != nil {
				t.Fatal(err)
			}
			if len(name) > 7 && name[:7] == "credit_" {
				t.Errorf("retired column %s.%s remains", table, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
}
