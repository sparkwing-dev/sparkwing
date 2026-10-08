package store_test

import (
	"context"
	"errors"
	"strconv"
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
	// safety: a future legacy timestamp pins clock regression without depending
	// on the platform clock's resolution or elapsed test time.
	previous := time.Now().Add(24 * time.Hour).UnixNano()
	_, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)`),
		"crons.tick.claim", "0", previous)
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("tick failed")
	for i := int64(1); i <= 2; i++ {
		ran, err := st.RunCronTickLeased(ctx, "fixture", time.Minute, func(context.Context) error {
			var token string
			if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st,
				`SELECT value FROM sparkwing_meta WHERE key = ?`), "crons.tick.claim").Scan(&token); err != nil {
				t.Fatal(err)
			}
			if want := strconv.FormatInt(previous+i, 10); token != want {
				t.Fatalf("token = %s, want %s", token, want)
			}
			return failed
		})
		if !ran || !errors.Is(err, failed) {
			t.Fatalf("tick: ran=%v err=%v", ran, err)
		}
		var value string
		var generation int64
		if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st,
			`SELECT value, updated_at FROM sparkwing_meta WHERE key = ?`), "crons.tick.claim").Scan(&value, &generation); err != nil {
			t.Fatal(err)
		}
		if value != "0" || generation != previous+i {
			t.Fatalf("released claim = %s/%d, want 0/%d", value, generation, previous+i)
		}
	}
}
