package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSweepClaimClearKeepsNewerOwner(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	claimed, oldToken, err := store.claimSweepWindowAt(
		ctx, metaKeyConcurrencySwept, metaKeyConcurrencySweepClaim, time.Hour, time.Nanosecond, time.Unix(1700000000, 0),
	)
	if err != nil {
		t.Fatalf("claim old: %v", err)
	}
	if !claimed {
		t.Fatalf("old claim did not run")
	}
	res, err := store.DB().ExecContext(ctx,
		`UPDATE sparkwing_meta SET value = ? WHERE key = ? AND value = ?`,
		"0", metaKeyConcurrencySweepClaim, oldToken,
	)
	if err != nil {
		t.Fatalf("expire old claim: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("count expired claims: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expired claims = %d, want 1", changed)
	}
	claimed, newToken, err := store.claimSweepWindowAt(
		ctx, metaKeyConcurrencySwept, metaKeyConcurrencySweepClaim, time.Hour, time.Nanosecond, time.Unix(1700000000, 0),
	)
	if err != nil {
		t.Fatalf("claim new: %v", err)
	}
	if !claimed {
		t.Fatalf("new claim did not run after old claim expired")
	}
	if oldToken == newToken {
		t.Fatal("repeated clock reused the old token")
	}
	if err := store.clearSweepClaim(ctx, metaKeyConcurrencySweepClaim, oldToken); err != nil {
		t.Fatalf("clear old claim: %v", err)
	}
	var value string
	if err := store.DB().QueryRow(
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyConcurrencySweepClaim,
	).Scan(&value); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	if value != newToken {
		t.Fatalf("claim value = %q, want newer token %q", value, newToken)
	}
}

func TestSweepClaimGenerationSurvivesReleaseAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	claimed, oldToken, err := st.claimSweepWindowAt(ctx, "stamp", "claim", 0, time.Minute, now)
	if err != nil || !claimed {
		t.Fatalf("first claim: %v %v", claimed, err)
	}
	if err := st.clearSweepClaim(ctx, "claim", oldToken); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	claimed, newToken, err := st.claimSweepWindowAt(ctx, "stamp", "claim", 0, time.Minute, now.Add(-time.Second))
	if err != nil || !claimed {
		t.Fatalf("reopened claim: %v %v", claimed, err)
	}
	oldNS, err := strconv.ParseInt(oldToken, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if newToken != strconv.FormatInt(oldNS+1, 10) {
		t.Fatalf("generation = %s, want %d", newToken, oldNS+1)
	}
	if err := st.clearSweepClaim(ctx, "claim", oldToken); err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := st.claimSweepWindowAt(ctx, "stamp", "claim", 0, time.Minute, now); err != nil || claimed {
		t.Fatalf("stale clear released live claim: claimed=%v err=%v", claimed, err)
	}
	if claimed, _, err := st.claimSweepWindowAt(ctx, "stamp", "claim", 0, time.Minute, now.Add(2*time.Minute)); err != nil || !claimed {
		t.Fatalf("TTL did not release live claim: claimed=%v err=%v", claimed, err)
	}
}
