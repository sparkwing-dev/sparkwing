package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func holderExists(t *testing.T, s *store.Store, key, holderID string) bool {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(storetest.Rebind(s,
		`SELECT COUNT(*) FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		key, holderID,
	).Scan(&n); err != nil {
		t.Fatalf("count holder: %v", err)
	}
	return n > 0
}

func expireHolderLease(t *testing.T, s *store.Store, key, holderID string) {
	t.Helper()
	result, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`),
		time.Now().Add(-time.Minute).UnixNano(), key, holderID,
	)
	if err != nil {
		t.Fatalf("expire holder lease: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("expire holder lease rows affected: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expire holder lease matched %d rows, want 1", rows)
	}
}
