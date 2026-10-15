package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func freeTeam(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	tenant := teamHandle(t, st, team)
	if err := st.GrantFreeSlot(context.Background(), team, time.Now()); err != nil {
		t.Fatalf("%s's free slot: %v", team, err)
	}
	return tenant
}

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
