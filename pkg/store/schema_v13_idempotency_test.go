package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	got, err := up.FindTriggerByIdempotencyKey(ctx, "deploy", "k")
	if err != nil {
		t.Fatalf("FindTriggerByIdempotencyKey: %v", err)
	}
	if got.ID != "first" {
		t.Fatalf("key resolves to %q, want the original %q", got.ID, "first")
	}
}

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

func TestFindTriggerByIdempotencyKey_EmptyKeyIsNotAMatch(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "keyless", Pipeline: "build", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindTriggerByIdempotencyKey(ctx, "build", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty key lookup: err=%v, want ErrNotFound", err)
	}
}

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
	resolved, err := s.FindTriggerByIdempotencyKey(ctx, "deploy", "shared")
	if err != nil {
		t.Fatalf("FindTriggerByIdempotencyKey: %v", err)
	}
	if resolved.ID != winners[0] {
		t.Fatalf("key resolves to %q, want the winner %q", resolved.ID, winners[0])
	}
}

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

func TestSchemaV13_StampsVersionAndCreatesTheNamedIndex(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	got, err := s.CurrentSchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got < 13 || got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want ExpectedSchemaVersion %d, at or past the v13 that added the index",
			got, store.ExpectedSchemaVersion())
	}

	var sqlText string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`,
		store.TriggerIdempotencyIndexName).Scan(&sqlText); err != nil {
		t.Fatalf("index %s absent from sqlite_master: %v", store.TriggerIdempotencyIndexName, err)
	}
	for _, want := range []string{"UNIQUE", "triggers", "pipeline", "idempotency_key", "WHERE"} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("index definition missing %q: %s", want, sqlText)
		}
	}
}

func TestSchemaV13_PostgresCarriesTheSameConstraint(t *testing.T) {
	s := openPGTestStore(t)
	ctx := context.Background()

	got, err := s.CurrentSchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != store.ExpectedSchemaVersion() {
		t.Fatalf("postgres schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}

	var indexDef string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT indexdef FROM pg_indexes
		  WHERE indexname = $1 AND schemaname = current_schema()`,
		store.TriggerIdempotencyIndexName).Scan(&indexDef); err != nil {
		t.Fatalf("index %s absent from this schema's pg_indexes: %v",
			store.TriggerIdempotencyIndexName, err)
	}

	for _, want := range []string{"UNIQUE", "triggers", "pipeline, idempotency_key", "WHERE"} {
		if !strings.Contains(indexDef, want) {
			t.Errorf("postgres index definition missing %q: %s", want, indexDef)
		}
	}

	now := time.Now()
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "pg-first", Pipeline: "deploy", CreatedAt: now, IdempotencyKey: "k",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "pg-dup", Pipeline: "deploy", CreatedAt: now, IdempotencyKey: "k",
	}); !errors.Is(err, store.ErrDuplicateIdempotencyKey) {
		t.Fatalf("postgres duplicate insert: err=%v, want ErrDuplicateIdempotencyKey", err)
	}

	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "pg-other", Pipeline: "other", CreatedAt: now, IdempotencyKey: "k",
	}); err != nil {
		t.Fatalf("postgres rejected one key across two pipelines: %v", err)
	}
}

func TestIdempotencyKeysAreScopedToTheirPipeline(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	now := time.Now()

	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "run-alpha", Pipeline: "alpha", CreatedAt: now, IdempotencyKey: "nightly",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "run-beta", Pipeline: "beta", CreatedAt: now, IdempotencyKey: "nightly",
	}); err != nil {
		t.Fatalf("one key used by two pipelines was rejected: %v", err)
	}

	alpha, err := s.FindTriggerByIdempotencyKey(ctx, "alpha", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if alpha.ID != "run-alpha" {
		t.Fatalf("alpha's key resolved to %q", alpha.ID)
	}
	beta, err := s.FindTriggerByIdempotencyKey(ctx, "beta", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if beta.ID != "run-beta" {
		t.Fatalf("beta's key resolved to %q, want its own run", beta.ID)
	}

	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "run-alpha-2", Pipeline: "alpha", CreatedAt: now, IdempotencyKey: "nightly",
	}); !errors.Is(err, store.ErrDuplicateIdempotencyKey) {
		t.Fatalf("duplicate within one pipeline: err=%v, want ErrDuplicateIdempotencyKey", err)
	}
}

func expireTriggerClaim(t *testing.T, s *store.Store, id string) {
	t.Helper()
	res, err := s.DB().Exec(
		`UPDATE triggers SET lease_expires_at = ? WHERE id = ? AND status = 'claimed' AND lease_expires_at IS NOT NULL`,
		time.Now().Add(-time.Second).UnixNano(), id,
	)
	if err != nil {
		t.Fatalf("expire trigger claim: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("count expired trigger claims: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expired trigger claims = %d, want 1", changed)
	}
}

func TestClaimGeneration_AdvancesOnEveryClaim(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-gen", "deploy")

	first, err := s.ClaimNextTrigger(ctx, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if first.ClaimSeq != 1 {
		t.Fatalf("first claim generation = %d, want 1", first.ClaimSeq)
	}
	expireTriggerClaim(t, s, "run-gen")
	if _, err := s.RequeueUnstartedClaim(ctx, "run-gen"); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.ClaimSeq != 2 {
		t.Fatalf("second claim generation = %d, want 2", second.ClaimSeq)
	}
}

func TestFinishAtGeneration_RefusesASupersededDispatch(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-fence", "deploy")

	stale, err := s.ClaimNextTrigger(ctx, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	expireTriggerClaim(t, s, "run-fence")
	if _, err := s.RequeueUnstartedClaim(ctx, "run-fence"); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	ok, err := s.FinishRunAtGeneration(ctx, "run-fence", stale.ClaimSeq, "failed", "stale writer")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a superseded dispatch was allowed to write the run's outcome")
	}
	done, err := s.FinishTriggerAtGeneration(ctx, "run-fence", stale.ClaimSeq)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("a superseded dispatch closed out a claim it no longer held")
	}
	run, err := s.GetRun(ctx, "run-fence")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending" {
		t.Fatalf("run status = %q; the stale writer changed it", run.Status)
	}

	ok, err = s.FinishRunAtGeneration(ctx, "run-fence", fresh.ClaimSeq, "success", "")
	if err != nil || !ok {
		t.Fatalf("current claim could not write its outcome: ok=%v err=%v", ok, err)
	}
}

func TestRequeueUnstartedClaim_LeavesARunningRunAlone(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-live", "deploy")
	if _, err := s.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-live", Pipeline: "deploy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	requeued, err := s.RequeueUnstartedClaim(ctx, "run-live")
	if err != nil {
		t.Fatal(err)
	}
	if requeued {
		t.Fatal("a running run was swept back onto the queue; it would execute twice")
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the running run became claimable again (err=%v)", err)
	}
}

func TestRequeueUnstartedClaim_RecoversARunThatNeverStarted(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-neverstarted", "deploy")
	if _, err := s.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}

	requeued, err := s.RequeueUnstartedClaim(ctx, "run-neverstarted")
	if err != nil {
		t.Fatal(err)
	}
	if !requeued {
		t.Fatal("a run that never started was not recovered")
	}
	got, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("recovered run is not claimable: %v", err)
	}
	if got.ID != "run-neverstarted" {
		t.Fatalf("claimed %q", got.ID)
	}
}
