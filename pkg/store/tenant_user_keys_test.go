package store_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A row written before v51 has to survive the rebuild. Several lookups
// on these tables answer a miss with a default rather than an error --
// no pin, no floor, no binding -- so a rebuild that dropped the rows
// would read as "nothing configured" and pass a suite that only ever
// writes after migrating. The narrowing here is the widening helper run
// backwards, so the test cannot pass against a migration that does not
// rebuild.
func TestSchemaV51CarriesPreExistingRowsThroughTheRebuild(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)

	seeds := map[string]string{
		"secrets": `INSERT INTO secrets (team, name, value, principal, masked, pipeline, shared, created_at, updated_at)
		            VALUES ('default', 'DEPLOY_KEY', 'v', 'admin', 1, 'ci', 0, 1, 1)`,
		"github_webhook_bindings": `INSERT INTO github_webhook_bindings (team, pipeline, repo, secret, events, hook_id, created_at, updated_at)
		            VALUES ('default', 'ci', 'acme/app', 's', 'push', 7, 1, 1)`,
		"pipeline_profiles": `INSERT INTO pipeline_profiles (team, pipeline, node_id, p50_duration_ms, p99_duration_ms, peak_cores, peak_memory_bytes, sample_count, updated_at, pinned_cores, pinned_memory_bytes)
		            VALUES ('default', 'ci', 'build', 10, 20, 2, 1024, 3, 1, 4, 2048)`,
		"concurrency_entries": `INSERT INTO concurrency_entries (team, key, capacity, updated_at)
		            VALUES ('default', 'deploy', 1, 1)`,
		"concurrency_holders": `INSERT INTO concurrency_holders (team, key, holder_id, run_id, node_id, claimed_at, lease_expires_at)
		            VALUES ('default', 'deploy', 'h1', 'r1', 'n1', 1, 9223372036854775807)`,
		"concurrency_waiters": `INSERT INTO concurrency_waiters (team, key, run_id, node_id, arrived_at, policy)
		            VALUES ('default', 'deploy', 'r2', 'n2', 1, 'queue')`,
		"concurrency_cache": `INSERT INTO concurrency_cache (team, key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
		            VALUES ('default', 'deploy', 'hash', 'r1/n1', 'r1', 'n1', 1, 9223372036854775807, 1)`,
	}

	narrow := store.UserKeyTablesForTest()
	tables := make([]string, 0, len(narrow))
	for table := range narrow {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	for _, table := range tables {
		if err := store.RekeyForTest(ctx, st, table, narrow[table]); err != nil {
			t.Fatalf("narrow %s back to the v49 key: %v", table, err)
		}
		if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, seeds[table])); err != nil {
			t.Fatalf("seed %s under the v49 key: %v", table, err)
		}
	}

	if err := store.ApplyUserKeyMigrationForTest(ctx, st); err != nil {
		t.Fatalf("apply v51: %v", err)
	}

	for _, table := range tables {
		var rows int
		q := storetest.Rebind(st, `SELECT COUNT(*) FROM `+table+` WHERE team = ?`)
		if err := st.DB().QueryRowContext(ctx, q, string(store.DefaultTeam)).Scan(&rows); err != nil {
			t.Fatalf("count %s after v51: %v", table, err)
		}
		if rows != 1 {
			t.Errorf("%s has %d rows in the default team after v51, want the seeded 1", table, rows)
		}
	}

	tn, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := tn.GetPipelineProfile(ctx, "ci", "build")
	if err != nil {
		t.Fatalf("GetPipelineProfile after v51: %v", err)
	}
	if profile == nil || profile.PinnedCores != 4 {
		t.Errorf("profile after v51 = %+v, want the pre-existing pin of 4 cores", profile)
	}
	binding, err := tn.GetGitHubWebhookBinding(ctx, "ci", "acme/app")
	if err != nil {
		t.Fatalf("GetGitHubWebhookBinding after v51: %v", err)
	}
	if binding.HookID != 7 {
		t.Errorf("binding after v51 = %+v, want the pre-existing hook 7", binding)
	}
	secret, err := tn.GetSecretRow("DEPLOY_KEY", "ci")
	if err != nil {
		t.Fatalf("GetSecretRow after v51: %v", err)
	}
	if secret.Value != "v" {
		t.Errorf("secret after v51 = %+v, want the pre-existing value", secret)
	}
}

func TestSecretNameIsUniquePerTeam(t *testing.T) {
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	now := time.Now()

	for _, pipeline := range []string{"", "ci"} {
		if err := alpha.CreateOrReplaceSecret(store.Secret{
			Name: "DEPLOY_KEY", Value: "alpha-" + pipeline, Pipeline: pipeline, Shared: true,
		}, now); err != nil {
			t.Fatalf("alpha DEPLOY_KEY on pipeline %q: %v", pipeline, err)
		}
		if err := beta.CreateOrReplaceSecret(store.Secret{
			Name: "DEPLOY_KEY", Value: "beta-" + pipeline, Pipeline: pipeline, Shared: true,
		}, now); err != nil {
			t.Fatalf("beta DEPLOY_KEY on pipeline %q: %v", pipeline, err)
		}
	}

	for _, c := range []struct {
		tn   *store.Tenant
		want string
	}{{alpha, "alpha-ci"}, {beta, "beta-ci"}} {
		got, err := c.tn.GetSecretForRun("DEPLOY_KEY", "ci")
		if err != nil {
			t.Fatalf("%s GetSecretForRun: %v", c.tn.Team(), err)
		}
		if got.Value != c.want {
			t.Errorf("%s reads DEPLOY_KEY = %q, want %q", c.tn.Team(), got.Value, c.want)
		}
	}

	rows, err := alpha.ListSecrets()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Value != "alpha-" && row.Value != "alpha-ci" {
			t.Errorf("alpha's listing carries %q, which is not alpha's", row.Value)
		}
	}

	if err := alpha.DeleteSecret("DEPLOY_KEY", "ci"); err != nil {
		t.Fatalf("alpha DeleteSecret: %v", err)
	}
	if _, err := alpha.GetSecretRow("DEPLOY_KEY", "ci"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alpha reads its deleted secret: %v", err)
	}
	got, err := beta.GetSecretRow("DEPLOY_KEY", "ci")
	if err != nil {
		t.Fatalf("beta lost its secret to alpha's delete: %v", err)
	}
	if got.Value != "beta-ci" {
		t.Errorf("beta's secret = %q after alpha deleted its own", got.Value)
	}
}

func TestGitHubWebhookBindingIsUniquePerTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")

	for _, c := range []struct {
		tn     *store.Tenant
		secret string
		hookID int64
	}{{alpha, "alpha-secret", 1}, {beta, "beta-secret", 2}} {
		if err := c.tn.PutGitHubWebhookBinding(ctx, store.GitHubWebhookBinding{
			Pipeline: "ci", Repo: "acme/app", Secret: c.secret, HookID: c.hookID,
		}); err != nil {
			t.Fatalf("%s PutGitHubWebhookBinding: %v", c.tn.Team(), err)
		}
	}

	for _, c := range []struct {
		tn     *store.Tenant
		secret string
	}{{alpha, "alpha-secret"}, {beta, "beta-secret"}} {
		got, err := c.tn.GetGitHubWebhookBinding(ctx, "ci", "acme/app")
		if err != nil {
			t.Fatalf("%s GetGitHubWebhookBinding: %v", c.tn.Team(), err)
		}
		if got.Secret != c.secret {
			t.Errorf("%s reads the binding secret %q, want %q", c.tn.Team(), got.Secret, c.secret)
		}
	}

	removed, err := alpha.DeleteGitHubWebhookBinding(ctx, "ci", "acme/app")
	if err != nil || !removed {
		t.Fatalf("alpha DeleteGitHubWebhookBinding = %v, %v", removed, err)
	}
	if _, err := beta.GetGitHubWebhookBinding(ctx, "ci", "acme/app"); err != nil {
		t.Errorf("beta lost its binding to alpha's delete: %v", err)
	}
	list, err := beta.ListGitHubWebhookBindings(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Secret != "beta-secret" {
		t.Errorf("beta's listing = %+v, want only its own binding", list)
	}
}

func TestPipelineProfileIsUniquePerTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")

	if err := alpha.UpsertProfilePin(ctx, "ci", "build", 4, 4096); err != nil {
		t.Fatalf("alpha UpsertProfilePin: %v", err)
	}
	if err := beta.UpsertProfilePin(ctx, "ci", "build", 1, 1024); err != nil {
		t.Fatalf("beta UpsertProfilePin: %v", err)
	}

	for _, c := range []struct {
		tn    *store.Tenant
		cores float64
	}{{alpha, 4}, {beta, 1}} {
		got, err := c.tn.GetPipelineProfile(ctx, "ci", "build")
		if err != nil {
			t.Fatalf("%s GetPipelineProfile: %v", c.tn.Team(), err)
		}
		if got == nil || got.PinnedCores != c.cores {
			t.Errorf("%s reads pinned cores %+v, want %v", c.tn.Team(), got, c.cores)
		}
	}

	if err := alpha.RecordProfileObservation(ctx, "ci", "build", store.ProfileObservation{
		Duration: time.Second, PeakCores: 3, PeakMemoryBytes: 2048, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("alpha RecordProfileObservation: %v", err)
	}
	betaProfile, err := beta.GetPipelineProfile(ctx, "ci", "build")
	if err != nil {
		t.Fatal(err)
	}
	if betaProfile.SampleCount != 0 {
		t.Errorf("beta's profile took alpha's sample: %+v", betaProfile)
	}

	if _, err := alpha.ResetAllProfiles(ctx); err != nil {
		t.Fatalf("alpha ResetAllProfiles: %v", err)
	}
	betaProfile, err = beta.GetPipelineProfile(ctx, "ci", "build")
	if err != nil {
		t.Fatal(err)
	}
	if betaProfile == nil || betaProfile.PinnedCores != 1 {
		t.Errorf("beta's pin = %+v after alpha reset every profile of its own team", betaProfile)
	}
	list, err := beta.ListPipelineProfiles(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PinnedCores != 1 {
		t.Errorf("beta's listing = %+v, want only its own profile", list)
	}
}

func seedConcurrencyRun(t *testing.T, tn *store.Tenant, id string) {
	t.Helper()
	now := time.Now()
	if err := tn.CreateRun(context.Background(), store.Run{
		ID: id, Pipeline: "ci", Status: "running", StartedAt: now,
		LastHeartbeatAt: &now,
	}); err != nil {
		t.Fatalf("CreateRun(%s): %v", id, err)
	}
}

// A concurrency key is authored in pipeline YAML, so two teams running
// the same pipeline share the spelling. Capacity, holders and waiters
// have to be counted per team, or one team's deploy lock holds the
// other's deploy back.
func TestConcurrencyKeyIsUniquePerTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	seedConcurrencyRun(t, alpha, "a1")
	seedConcurrencyRun(t, beta, "b1")

	for _, c := range []struct {
		tn    *store.Tenant
		runID string
	}{{alpha, "a1"}, {beta, "b1"}} {
		got, err := c.tn.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
			Key: "deploy", HolderID: c.runID + "/n1", RunID: c.runID, NodeID: "n1",
			Capacity: 1, Policy: store.OnLimitQueue, Lease: time.Hour,
		})
		if err != nil {
			t.Fatalf("%s acquire: %v", c.tn.Team(), err)
		}
		if got.Kind != store.AcquireGranted {
			t.Fatalf("%s acquire on a capacity-1 key another team holds = %q, want granted",
				c.tn.Team(), got.Kind)
		}
	}

	for _, c := range []struct {
		tn     *store.Tenant
		holder string
	}{{alpha, "a1/n1"}, {beta, "b1/n1"}} {
		state, err := c.tn.GetConcurrencyState(ctx, "deploy")
		if err != nil {
			t.Fatalf("%s GetConcurrencyState: %v", c.tn.Team(), err)
		}
		if len(state.Holders) != 1 || state.Holders[0].HolderID != c.holder {
			t.Errorf("%s sees holders %+v, want only %q", c.tn.Team(), state.Holders, c.holder)
		}
		if state.UsedCost != 1 {
			t.Errorf("%s sees used cost %d, want 1", c.tn.Team(), state.UsedCost)
		}
	}

	if _, err := alpha.ReleaseConcurrencySlot(ctx, "deploy", "a1/n1", "success", "", "", 0); err != nil {
		t.Fatalf("alpha release: %v", err)
	}
	if _, err := beta.ActiveConcurrencyHolder(ctx, "deploy", "b1/n1", time.Now()); err != nil {
		t.Errorf("beta lost its holder to alpha's release: %v", err)
	}
}

// The memo cache is the read leak the widened key closes. The write was
// ON CONFLICT (key, cache_key_hash) and the read returned output_ref,
// origin_run_id and origin_node_id, so two teams that author one
// concurrency key and hash the same inputs handed each other pointers
// into one another's outputs. Proving the write no longer collides is
// not enough: the read is what leaked.
func TestConcurrencyCacheDoesNotServeAnotherTeamsOutput(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	seedConcurrencyRun(t, alpha, "a1")
	seedConcurrencyRun(t, beta, "b1")

	if got, err := alpha.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "memo", HolderID: "a1/n1", RunID: "a1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: "same-hash", Lease: time.Hour,
	}); err != nil || got.Kind != store.AcquireGranted {
		t.Fatalf("alpha acquire = %+v, %v; want granted", got, err)
	}
	if released, _, _, err := alpha.ReleaseAndNotify(ctx, "memo", "a1/n1", "success",
		"a1/n1/out", "same-hash", time.Hour, time.Hour); err != nil || !released {
		t.Fatalf("alpha release = %v, %v; want released", released, err)
	}

	got, err := beta.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "memo", HolderID: "b1/n1", RunID: "b1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: "same-hash", Lease: time.Hour,
	})
	if err != nil {
		t.Fatalf("beta acquire: %v", err)
	}
	if got.Kind == store.AcquireCached {
		t.Fatalf("beta was served alpha's memo: output %q from run %q node %q",
			got.OutputRef, got.OriginRunID, got.OriginNodeID)
	}
	if got.OutputRef != "" || got.OriginRunID != "" {
		t.Errorf("beta's acquire carries alpha's origin: %+v", got)
	}

	res, err := beta.ResolveWaiter(ctx, "memo", "b1", "n1", "same-hash", "", "", false)
	if err != nil {
		t.Fatalf("beta ResolveWaiter: %v", err)
	}
	if res.Status == store.WaiterCached {
		t.Fatalf("beta's poll was served alpha's memo: output %q from run %q node %q",
			res.OutputRef, res.OriginRunID, res.OriginNodeID)
	}

	seedConcurrencyRun(t, alpha, "a2")
	again, err := alpha.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "memo", HolderID: "a2/n1", RunID: "a2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: "same-hash", Lease: time.Hour,
	})
	if err != nil {
		t.Fatalf("alpha re-acquire: %v", err)
	}
	if again.Kind != store.AcquireCached || again.OutputRef != "a1/n1/out" {
		t.Errorf("alpha's own memo = %+v, want its cached output a1/n1/out", again)
	}
}
