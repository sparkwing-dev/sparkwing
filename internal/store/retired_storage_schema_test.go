package store

import "testing"

func TestStoreHasNoRetiredStorageLedgers(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"storage_reservations", "storage_commit_receipts"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("retired table %s still exists", name)
		}
	}
}
