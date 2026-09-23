package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A run's retention window starts when it finishes. The long run was created
// before the window but finished inside it, so a sweep keyed on creation
// would delete a run that just ended; this one keeps it.
func TestRetentionReleasesRunsByWhenTheyFinished(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	day := 24 * time.Hour
	for _, run := range []struct {
		id                string
		created, finished time.Duration
	}{
		{"old", 40 * day, 31 * day},
		{"long", 40 * day, day},
		{"recent", 2 * day, day},
	} {
		seedRunWithNode(t, st, run.id, "n1", "running")
		if _, err := st.AppendEvent(ctx, run.id, "n1", "k", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().Exec(storetest.Rebind(st,
			`UPDATE runs SET status = 'success', created_at = ?, finished_at = ? WHERE id = ?`),
			now.Add(-run.created).UnixNano(), now.Add(-run.finished).UnixNano(), run.id); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().Exec(storetest.Rebind(st,
			`INSERT INTO storage_run_usage (principal, run_id, bytes, objects, updated_at) VALUES ('ci', ?, 100, 1, ?)`),
			run.id, now.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}

	if expired, err := st.ExpireRetainedRuns(ctx, now); err != nil || len(expired) != 0 {
		t.Fatalf("expiry with retention off = %+v, %v; want nothing", expired, err)
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	expired, err := st.ExpireRetainedRuns(ctx, now)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || expired[0].Runs != 1 || expired[0].Bytes != 100 {
		t.Fatalf("expired = %+v, want the one run that finished 31 days ago", expired)
	}
	for id, want := range map[string]int64{"old": 0, "long": 1, "recent": 1} {
		if n := countRows(t, st, `SELECT COUNT(*) FROM events WHERE run_id = '`+id+`'`); n != want {
			t.Errorf("run %s keeps %d events, want %d", id, n, want)
		}
	}
	if again, err := st.ExpireRetainedRuns(ctx, now); err != nil || len(again) != 0 {
		t.Fatalf("a second pass = %+v, %v; want nothing left to expire", again, err)
	}
}

func TestSeedStorageRetentionKeepsAWindowTheOperatorSet(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if seeded, err := st.SeedStorageRetention(ctx, 30); err != nil || !seeded {
		t.Fatalf("first seed = %v, %v; want written", seeded, err)
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 0, NodeMetricRetentionDays: 0}); err != nil {
		t.Fatal(err)
	}
	if seeded, err := st.SeedStorageRetention(ctx, 30); err != nil || seeded {
		t.Fatalf("second seed = %v, %v; want the operator's zero kept", seeded, err)
	}
	settings, err := st.StorageSettings(ctx)
	if err != nil || settings.EventRetentionDays != 0 {
		t.Fatalf("settings = %+v, %v; want retention off as the operator set it", settings, err)
	}
}
