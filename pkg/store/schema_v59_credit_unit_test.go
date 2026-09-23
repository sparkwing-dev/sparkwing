package store_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A runner scale step written when a credit was a cent keeps its dollar value
// when the credit becomes a vCPU-second: 5,000 cent-credits, fifty dollars,
// become 1,000,000 vCPU-second credits, which is still fifty dollars.
func TestSchemaV59_RestatesTheRunnerScaleStepInTheNewCredit(t *testing.T) {
	cases := []struct {
		name string
		old  string
		want string
	}{
		{name: "a step", old: "5000", want: "1000000"},
		{name: "the old ceiling", old: "1000000000", want: "200000000000"},
		{name: "scaling off", old: "0", want: "0"},
		{name: "unreadable", old: "fifty", want: "fifty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := storetest.New(t)
			seeded, err := target.TryOpen()
			if err != nil {
				t.Fatalf("Open#1: %v", err)
			}
			ctx := context.Background()
			writeStepSetting(t, seeded, tc.old)
			if _, err := seeded.DB().ExecContext(ctx,
				`DELETE FROM sparkwing_schema_version WHERE version >= 59`); err != nil {
				t.Fatalf("stamp v58: %v", err)
			}
			_ = seeded.Close()

			upgraded, err := target.TryOpen()
			if err != nil {
				t.Fatalf("Open#2 (upgrade): %v", err)
			}
			if got := readStepSetting(t, upgraded); got != tc.want {
				t.Fatalf("step after upgrade = %q, want %q", got, tc.want)
			}
			if v, err := upgraded.CurrentSchemaVersion(ctx); err != nil || v != 60 || v != store.ExpectedSchemaVersion() {
				t.Fatalf("schema after upgrade = %d, %v; want 60", v, err)
			}
			_ = upgraded.Close()

			// safety: the ladder records v59, so a second open must not scale the
			// step again; a migration that ran twice would sell steps 200 times
			// too cheap.
			reopened, err := target.TryOpen()
			if err != nil {
				t.Fatalf("Open#3: %v", err)
			}
			defer func() { _ = reopened.Close() }()
			if got := readStepSetting(t, reopened); got != tc.want {
				t.Fatalf("step after a second open = %q, want %q", got, tc.want)
			}
		})
	}
}

// A store that never set the step holds no row, and the migration writes none.
func TestSchemaV59_LeavesAnUnsetStepUnset(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if _, err := seeded.DB().ExecContext(ctx,
		`DELETE FROM sparkwing_schema_version WHERE version >= 59`); err != nil {
		t.Fatalf("stamp v58: %v", err)
	}
	_ = seeded.Close()
	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	var n int
	if err := upgraded.DB().QueryRowContext(ctx, storetest.Rebind(upgraded,
		`SELECT COUNT(*) FROM sparkwing_meta WHERE key = ?`), stepKey).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("an unset step gained %d rows", n)
	}
}

const stepKey = "compute_limit_" + store.ComputeLimitRunnerScaleStepCredits

func writeStepSetting(t *testing.T, st *store.Store, value string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(), storetest.Rebind(st,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)`),
		stepKey, value, int64(1)); err != nil {
		t.Fatalf("write step: %v", err)
	}
}

func readStepSetting(t *testing.T, st *store.Store) string {
	t.Helper()
	var raw string
	if err := st.DB().QueryRowContext(context.Background(), storetest.Rebind(st,
		`SELECT value FROM sparkwing_meta WHERE key = ?`), stepKey).Scan(&raw); err != nil {
		t.Fatalf("read step: %v", err)
	}
	return raw
}
