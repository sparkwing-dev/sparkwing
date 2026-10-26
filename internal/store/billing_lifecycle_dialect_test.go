package store_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestBillingLifecycleReferenceModel(t *testing.T) {
	for _, dialect := range []struct {
		name string
		open func(*testing.T) *store.Store
	}{{"sqlite", func(t *testing.T) *store.Store { return storetest.OpenSQLite(t) }}, {"postgres", func(t *testing.T) *store.Store { return storetest.OpenPostgres(t) }}} {
		t.Run(dialect.name, func(t *testing.T) { store.BillingLifecycleReferenceModel(t, dialect.open) })
	}
}

func TestReversalReferenceConstraintBothDialects(t *testing.T) {
	for _, open := range []struct {
		name string
		open func(*testing.T) *store.Store
	}{{"sqlite", func(t *testing.T) *store.Store { return storetest.OpenSQLite(t) }}, {"postgres", func(t *testing.T) *store.Store { return storetest.OpenPostgres(t) }}} {
		t.Run(open.name, func(t *testing.T) {
			s := open.open(t)
			if _, err := s.DB().ExecContext(t.Context(), `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_at) VALUES ('default','original','paid',10,'existing',1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().ExecContext(t.Context(), `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,reverses,created_at) VALUES ('default','valid','reversal',-1,'valid-refund','existing',1)`); err != nil {
				t.Fatal(err)
			}
			query := `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,reverses,created_at) VALUES ('default','bad','reversal',-1,'refund','missing',1)`
			if _, err := s.DB().ExecContext(t.Context(), query); err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
				t.Fatalf("missing grant foreign key refusal: %v", err)
			}
		})
	}
}
