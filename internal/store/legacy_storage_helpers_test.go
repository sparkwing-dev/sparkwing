package store_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

type legacyStorageUsage struct{ UsedBytes, ReservedBytes int64 }

func usageOf(t *testing.T, st *store.Store, team store.Team, kind store.StorageKind) legacyStorageUsage {
	t.Helper()
	var out legacyStorageUsage
	err := st.DB().QueryRowContext(t.Context(), storetest.Rebind(st, "SELECT used_bytes,reserved_bytes FROM team_storage WHERE team=? AND store=?"), team, kind).Scan(&out.UsedBytes, &out.ReservedBytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("storage of %s: %v", team, err)
	}
	return out
}
