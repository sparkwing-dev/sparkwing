package store_test

import (
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"testing"
)

func TestCloudMemoBillingPostgres(t *testing.T) {
	store.CloudMemoBilling(t, func(t *testing.T) *store.Store {
		s := storetest.OpenPostgres(t)
		if _, err := s.DB().ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)`); err != nil {
			t.Fatal(err)
		}
		return s
	})
}
