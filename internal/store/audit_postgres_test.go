package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAuditAppendOnlyPostgres(t *testing.T) {
	s := storetest.NewPostgres(t).Open(t)
	if _, err := s.DB().Exec(`INSERT INTO audit_events(id,ts,kind) VALUES ('audit-probe',1,'probe')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE audit_events SET actor='forged'`, `DELETE FROM audit_events`, `TRUNCATE audit_events`} {
		if _, err := s.DB().Exec(q); err == nil {
			t.Fatalf("audit mutation accepted: %s", q)
		}
	}
}
