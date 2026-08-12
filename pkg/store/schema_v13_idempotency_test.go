package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestSchemaV13_UpgradeAddsIdempotencyKeyAndItsConstraint proves the
// migration gives an existing database both halves of dedup. The column
// alone would let two submissions of one key both insert, so the
// constraint is checked here rather than assumed from the fresh schema.
func TestSchemaV13_UpgradeAddsIdempotencyKeyAndItsConstraint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema12.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP INDEX IF EXISTS idx_triggers_idempotency_key`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE triggers DROP COLUMN idempotency_key`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 13`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v12 store: %v", err)
	}
	defer func() { _ = up.Close() }()
	ctx := context.Background()

	if err := up.CreateTrigger(ctx, store.Trigger{
		ID: "first", Pipeline: "deploy", CreatedAt: time.Now(), IdempotencyKey: "k",
	}); err != nil {
		t.Fatalf("CreateTrigger after upgrade: %v", err)
	}
	err = up.CreateTrigger(ctx, store.Trigger{
		ID: "second", Pipeline: "deploy", CreatedAt: time.Now(), IdempotencyKey: "k",
	})
	if !errors.Is(err, store.ErrDuplicateIdempotencyKey) {
		t.Fatalf("second insert under one key: err=%v, want ErrDuplicateIdempotencyKey", err)
	}
	got, err := up.FindTriggerByIdempotencyKey(ctx, "k")
	if err != nil {
		t.Fatalf("FindTriggerByIdempotencyKey: %v", err)
	}
	if got.ID != "first" {
		t.Fatalf("key resolves to %q, want the original %q", got.ID, "first")
	}
}

// TestCreateTrigger_EmptyIdempotencyKeyNeverCollides pins that the
// partial index exempts the default. Every trigger the webhook, spawn,
// and retry paths create carries the empty key; if those collided the
// whole product would stop after one trigger.
func TestCreateTrigger_EmptyIdempotencyKeyNeverCollides(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := s.CreateTrigger(ctx, store.Trigger{
			ID: id, Pipeline: "build", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("CreateTrigger %s with no key: %v", id, err)
		}
	}
}

// TestFindTriggerByIdempotencyKey_EmptyKeyIsNotAMatch guards the same
// exemption on the read side: an empty key must never resolve to some
// arbitrary keyless trigger.
func TestFindTriggerByIdempotencyKey_EmptyKeyIsNotAMatch(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "keyless", Pipeline: "build", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindTriggerByIdempotencyKey(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty key lookup: err=%v, want ErrNotFound", err)
	}
}

// TestCreateTrigger_ConcurrentSubmissionsOfOneKeyProduceOneRun is the
// duplicate-submission proof at the level that decides it. A
// read-then-write submitter would let two racing callers both see "no
// such key" and both insert; only the constraint makes exactly one win.
func TestCreateTrigger_ConcurrentSubmissionsOfOneKeyProduceOneRun(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []string
	var duplicates int

	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := "run-" + string(rune('a'+i))
			err := s.CreateTrigger(ctx, store.Trigger{
				ID: id, Pipeline: "deploy", CreatedAt: time.Now(), IdempotencyKey: "shared",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, id)
			case errors.Is(err, store.ErrDuplicateIdempotencyKey):
				duplicates++
			default:
				t.Errorf("unexpected insert error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("%d submissions created %d triggers, want exactly 1 (winners=%v)", racers, len(winners), winners)
	}
	if duplicates != racers-1 {
		t.Fatalf("losers reported %d duplicates, want %d", duplicates, racers-1)
	}
	resolved, err := s.FindTriggerByIdempotencyKey(ctx, "shared")
	if err != nil {
		t.Fatalf("FindTriggerByIdempotencyKey: %v", err)
	}
	if resolved.ID != winners[0] {
		t.Fatalf("key resolves to %q, want the winner %q", resolved.ID, winners[0])
	}
}

// TestCancelPendingTrigger_TerminatesTheQueuedRun covers cancelling a
// submitted run that no consumer has picked up: both rows must move, or
// the run reads as cancelled in one view and queued in another.
func TestCancelPendingTrigger_TerminatesTheQueuedRun(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-queued", "deploy")

	cancelled, err := s.CancelPendingTrigger(ctx, "run-queued")
	if err != nil {
		t.Fatalf("CancelPendingTrigger: %v", err)
	}
	if !cancelled {
		t.Fatal("CancelPendingTrigger reported no-op on a pending run")
	}

	trig, err := s.GetTrigger(ctx, "run-queued")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status == "pending" {
		t.Fatal("cancelled trigger is still claimable")
	}
	run, err := s.GetRun(ctx, "run-queued")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", run.Status)
	}
	if run.FinishedAt == nil {
		t.Fatal("cancelled run has no finish time")
	}
}

// TestCancelPendingTrigger_DoesNotStealAClaimedTrigger pins the guard
// that keeps cancellation from racing a consumer. Once a claim lands the
// pending path must decline, so the caller escalates to cancelling the
// running run instead of silently doing nothing.
func TestCancelPendingTrigger_DoesNotStealAClaimedTrigger(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-claimed", "deploy")
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}

	cancelled, err := s.CancelPendingTrigger(ctx, "run-claimed")
	if err != nil {
		t.Fatalf("CancelPendingTrigger: %v", err)
	}
	if cancelled {
		t.Fatal("cancelled a trigger a consumer had already claimed")
	}
	run, err := s.GetRun(ctx, "run-claimed")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending" {
		t.Fatalf("declined cancel still changed the run to %q", run.Status)
	}
}

// TestCancelPendingTrigger_CannotReachAReplacementRun is the
// exact-target requirement. Cancelling a run must never touch the run
// that replaced it, which is the failure mode an id-reusing design would
// have: here the replacement carries a distinct id and survives intact.
func TestCancelPendingTrigger_CannotReachAReplacementRun(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-original", "deploy")
	seedSubmittedRun(t, s, "run-replacement", "deploy")

	if _, err := s.CancelPendingTrigger(ctx, "run-original"); err != nil {
		t.Fatalf("CancelPendingTrigger: %v", err)
	}

	replacement, err := s.GetRun(ctx, "run-replacement")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Status != "pending" {
		t.Fatalf("replacement run status = %q, want it untouched at pending", replacement.Status)
	}
	claimed, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("replacement must still be claimable: %v", err)
	}
	if claimed.ID != "run-replacement" {
		t.Fatalf("claimed %q, want the surviving replacement", claimed.ID)
	}
}

// TestCancelPendingTrigger_UnknownRunIsANoOpNotAnError lets the CLI try
// the queued path first for every id without turning cluster runs and
// finished runs into errors.
func TestCancelPendingTrigger_UnknownRunIsANoOpNotAnError(t *testing.T) {
	s := newStoreT(t)
	cancelled, err := s.CancelPendingTrigger(context.Background(), "run-never-existed")
	if err != nil {
		t.Fatalf("CancelPendingTrigger on unknown id: %v", err)
	}
	if cancelled {
		t.Fatal("reported cancelling a run that does not exist")
	}
}

// TestCountPendingTriggers_TracksTheClaimableQueue backs the consumer's
// idle-exit decision; a wrong count there strands acknowledged work.
func TestCountPendingTriggers_TracksTheClaimableQueue(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	if n, err := s.CountPendingTriggers(ctx); err != nil || n != 0 {
		t.Fatalf("empty queue: n=%d err=%v, want 0", n, err)
	}
	seedSubmittedRun(t, s, "run-1", "deploy")
	seedSubmittedRun(t, s, "run-2", "deploy")
	if n, err := s.CountPendingTriggers(ctx); err != nil || n != 2 {
		t.Fatalf("two queued: n=%d err=%v, want 2", n, err)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountPendingTriggers(ctx); err != nil || n != 1 {
		t.Fatalf("after one claim: n=%d err=%v, want 1", n, err)
	}
}

// seedSubmittedRun writes the trigger + pending run pair `runs submit`
// creates, so the store tests exercise the same row shape.
func seedSubmittedRun(t *testing.T, s *store.Store, id, pipeline string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: pipeline, CreatedAt: now, TriggerSource: "runs-submit",
	}); err != nil {
		t.Fatalf("seed trigger %s: %v", id, err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID: id, Pipeline: pipeline, Status: "pending",
		TriggerSource: "runs-submit", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatalf("seed run %s: %v", id, err)
	}
}
