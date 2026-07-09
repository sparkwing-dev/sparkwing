package store

import (
	"context"
	"path/filepath"
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
	claimed, oldToken, err := store.claimSweepWindow(
		ctx, metaKeyConcurrencySwept, metaKeyConcurrencySweepClaim, time.Hour, time.Nanosecond,
	)
	if err != nil {
		t.Fatalf("claim old: %v", err)
	}
	if !claimed {
		t.Fatalf("old claim did not run")
	}
	time.Sleep(time.Millisecond)
	claimed, newToken, err := store.claimSweepWindow(
		ctx, metaKeyConcurrencySwept, metaKeyConcurrencySweepClaim, time.Hour, time.Nanosecond,
	)
	if err != nil {
		t.Fatalf("claim new: %v", err)
	}
	if !claimed {
		t.Fatalf("new claim did not run after old claim expired")
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
