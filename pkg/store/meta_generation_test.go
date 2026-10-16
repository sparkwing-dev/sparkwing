package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSweepClaimGenerationSQLDialects(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testSweepClaimGeneration(t, storetest.OpenSQLite(t)) })
	t.Run("postgres", func(t *testing.T) { testSweepClaimGeneration(t, storetest.NewPostgres(t).Open(t)) })
}

func testSweepClaimGeneration(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	previous := int64(91)
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO leases(name,holder,until,generation) VALUES ('schedules.tick','',0,?)`), previous); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("tick failed")
	for i := int64(1); i <= 2; i++ {
		ran, err := st.AsOperator().RunCronTickLeased(ctx, "fixture", time.Minute, func(context.Context) error {
			var token int64
			if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st,
				`SELECT generation FROM leases WHERE name = ?`), "schedules.tick").Scan(&token); err != nil {
				t.Fatal(err)
			}
			if want := previous + i; token != want {
				t.Fatalf("token = %d, want %d", token, want)
			}
			return failed
		})
		if !ran || !errors.Is(err, failed) {
			t.Fatalf("tick: ran=%v err=%v", ran, err)
		}
		var until int64
		var generation int64
		if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st,
			`SELECT until,generation FROM leases WHERE name = ?`), "schedules.tick").Scan(&until, &generation); err != nil {
			t.Fatal(err)
		}
		if until != 0 || generation != previous+i {
			t.Fatalf("released claim = %d/%d, want 0/%d", until, generation, previous+i)
		}
	}
}
