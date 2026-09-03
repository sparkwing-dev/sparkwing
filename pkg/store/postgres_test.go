package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const requirePGEnv = "SPARKWING_REQUIRE_PG"

func pgTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SPARKWING_TEST_PG_URL")
	if dsn == "" {
		if os.Getenv(requirePGEnv) != "" {
			t.Fatalf("%s is set, so SPARKWING_TEST_PG_URL must name a reachable Postgres", requirePGEnv)
		}
		t.Skip("SPARKWING_TEST_PG_URL not set; skipping Postgres conformance test")
	}
	return dsn
}

func openPGTestStore(t *testing.T) *store.Store {
	t.Helper()
	scoped := pgTestSchemaDSN(t)
	st, err := store.OpenPostgres(context.Background(), scoped)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func pgTestSchemaDSN(t *testing.T) string {
	t.Helper()
	baseDSN := pgTestDSN(t)
	schema := "sw_test_" + sanitize(t.Name()) + "_" + uniq()

	adminCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := store.OpenPostgres(adminCtx, baseDSN)
	if err != nil {
		t.Fatalf("open admin postgres: %v", err)
	}
	if _, err := admin.DB().ExecContext(adminCtx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	_ = admin.Close()

	scoped := withSearchPath(baseDSN, schema)
	t.Cleanup(func() {
		if cleanup, e := store.OpenPostgres(context.Background(), baseDSN); e == nil {
			_, _ = cleanup.DB().Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
			_ = cleanup.Close()
		}
	})
	return scoped
}

var uniqCounter struct {
	sync.Mutex
	n int
}

func uniq() string {
	uniqCounter.Lock()
	defer uniqCounter.Unlock()
	uniqCounter.n++
	return fmt.Sprintf("%d_%d", time.Now().UnixNano()&0xffffff, uniqCounter.n)
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", " ", "_", "-", "_", ".", "_", "#", "_", "(", "_", ")", "_")
	out := r.Replace(s)
	if len(out) > 40 {
		out = out[:40]
	}
	return strings.ToLower(out)
}

func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%ssearch_path=%s", dsn, sep, schema)
}

func TestPostgresOpenAndMigrate(t *testing.T) {
	st := openPGTestStore(t)
	if got, want := st.Dialect(), store.DialectPostgres; got != want {
		t.Errorf("Dialect = %v, want %v", got, want)
	}
	ctx := context.Background()
	r := store.Run{
		ID:        "pg-open-test",
		Pipeline:  "p",
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := st.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.ID != r.ID {
		t.Fatalf("GetRun returned %+v, want id %q", got, r.ID)
	}
}

func TestSchemaV18_PostgresScrubsSecretInputHash(t *testing.T) {
	scoped := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "legacy-secret", Pipeline: "deploy", Status: "success", StartedAt: time.Now(),
		Args: map[string]string{"token": "low-entropy"},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": "low-entropy"},
			store.InvocationSecretArgsKey: []string{"token"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"args":{"token":"low-entropy"},"inputs_hash":"sha256:oracle","secret_args":["token"]}`)
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET invocation_json = $1 WHERE id = 'legacy-secret'`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM sparkwing_schema_version WHERE version >= 18`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	var raw []byte
	if err := up.DB().QueryRowContext(ctx,
		`SELECT invocation_json FROM runs WHERE id = 'legacy-secret'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "inputs_hash") {
		t.Errorf("Postgres migration retained secret inputs_hash: %s", raw)
	}
}

func TestSchemaV28_PostgresMigratesActualV27Shape(t *testing.T) {
	scoped := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE executors`,
		`ALTER TABLE nodes DROP COLUMN claim_executor`,
		`ALTER TABLE nodes DROP COLUMN claim_cores`,
		`ALTER TABLE nodes DROP COLUMN claim_memory_bytes`,
		`ALTER TABLE nodes DROP COLUMN claim_reservation`,
		`ALTER TABLE nodes DROP COLUMN claim_slot`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 28`,
	} {
		if _, err := st.DB().ExecContext(ctx, stmt); err != nil {
			_ = st.Close()
			t.Fatalf("downgrade with %q: %v", stmt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatalf("open v27 shape at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer up.Close()
	for _, name := range []string{"claim_executor", "claim_cores", "claim_memory_bytes", "claim_reservation", "claim_slot"} {
		var exists bool
		if err := up.DB().QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
   WHERE table_schema = current_schema() AND table_name = 'nodes' AND column_name = $1
)`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("migrated nodes missing %s", name)
		}
	}
	for _, column := range []struct{ table, name, want string }{
		{table: "nodes", name: "claim_cores", want: "double precision"},
		{table: "nodes", name: "claim_slot", want: "bigint"},
		{table: "executors", name: "budget_cores", want: "double precision"},
		{table: "executors", name: "headroom_cores", want: "double precision"},
	} {
		var dataType string
		if err := up.DB().QueryRowContext(ctx, `
SELECT data_type FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
			column.table, column.name).Scan(&dataType); err != nil {
			t.Fatal(err)
		}
		if dataType != column.want {
			t.Errorf("%s.%s type = %q, want %s", column.table, column.name, dataType, column.want)
		}
	}
	e := store.Executor{Name: "pg-a", Kind: "agent", Location: "unknown", MaxConcurrent: 1}
	if err := up.EnrollExecutor(ctx, "swr_pg_exact", e); err != nil {
		t.Fatalf("enroll migrated executor: %v", err)
	}
	e.Name = "pg-b"
	if err := up.EnrollExecutor(ctx, "swr_pg_exact", e); err == nil {
		t.Fatal("migrated Postgres schema accepted one credential for two executors")
	}
}

func TestPostgresClaimNextReadyNode_Concurrent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "r1", NodeID: "n1", Status: "ready",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	var winners int
	var losers int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			n, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, fmt.Sprintf("h-%d", id), time.Minute, nil)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && n != nil:
				winners++
			case errors.Is(err, store.ErrNotFound):
				losers++
			default:
				t.Errorf("unexpected: n=%v err=%v", n, err)
			}
		}(i)
	}
	wg.Wait()

	if winners != 1 {
		t.Errorf("winners = %d, want 1", winners)
	}
	if losers != 3 {
		t.Errorf("losers = %d, want 3", losers)
	}
}

func TestPostgresAcquireConcurrencySlot_Serializes(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	const key = "shared-slot"
	const n = 5

	for i := 0; i < n; i++ {
		runID := fmt.Sprintf("r-%d", i)
		if err := st.CreateRun(ctx, store.Run{
			ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("CreateRun %s: %v", runID, err)
		}
	}

	type result struct {
		kind store.AcquireKind
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
				Key:      key,
				RunID:    fmt.Sprintf("r-%d", i),
				NodeID:   "n",
				HolderID: fmt.Sprintf("h-%d", i),
				Capacity: 1,
				Policy:   store.OnLimitQueue,
				Lease:    time.Minute,
			})
			results[i] = result{kind: resp.Kind, err: err}
		}(i)
	}
	wg.Wait()

	var granted, queued int
	for _, r := range results {
		if r.err != nil {
			t.Errorf("unexpected error: %v", r.err)
			continue
		}
		switch r.kind {
		case store.AcquireGranted:
			granted++
		case store.AcquireQueued:
			queued++
		default:
			t.Errorf("unexpected acquire kind: %v", r.kind)
		}
	}
	if granted != 1 {
		t.Errorf("granted = %d, want 1", granted)
	}
	if queued != n-1 {
		t.Errorf("queued = %d, want %d", queued, n-1)
	}
}

func TestPostgresResolveWaiterPromotesOnceUnderConcurrentPoll(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	acquireT(t, st, store.AcquireSlotRequest{
		Key: "resolve-slot", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	for i := 0; i < 2; i++ {
		acquireT(t, st, store.AcquireSlotRequest{
			Key: "resolve-slot", HolderID: fmt.Sprintf("w-%d", i), RunID: fmt.Sprintf("waiter-%d", i), NodeID: "n",
			Capacity: 1, Policy: store.OnLimitQueue,
		})
	}
	if _, err := st.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = $1 AND holder_id = $2`,
		"resolve-slot", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	type result struct {
		resolution store.WaiterResolution
		err        error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resolution, err := st.ResolveWaiter(ctx, "resolve-slot", fmt.Sprintf("waiter-%d", i), "n", "", "", "", false)
			results[i] = result{resolution: resolution, err: err}
		}(i)
	}
	wg.Wait()

	var promoted, waiting int
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("resolve error: %v", result.err)
		}
		switch result.resolution.Status {
		case store.WaiterPromoted:
			promoted++
		case store.WaiterStillWaiting:
			waiting++
		default:
			t.Fatalf("unexpected resolution: %+v", result.resolution)
		}
	}
	if promoted != 1 || waiting != 1 {
		t.Fatalf("promoted=%d waiting=%d, want promoted=1 waiting=1", promoted, waiting)
	}
	state, err := st.GetConcurrencyState(ctx, "resolve-slot")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Holders) != 1 || state.Holders[0].RunID != "waiter-0" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 1 || state.Waiters[0].RunID != "waiter-1" {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}
