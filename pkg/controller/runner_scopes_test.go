package controller_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var runnerScopes = []string{
	controller.ScopeNodesClaim,
	controller.ScopeTriggersClaim,
	controller.ScopeRunsState,
	controller.ScopeSecretsRead,
	controller.ScopeLogsWrite,
}

type scopedFixture struct {
	url   string
	store *store.Store
}

func newScopedFixture(t *testing.T, scopes []string) (scopedFixture, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if _, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, now); err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	raw, _, err := st.CreateToken("pool", store.TokenKindRunner, scopes, 0, now)
	if err != nil {
		t.Fatalf("CreateToken runner: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return scopedFixture{url: srv.URL, store: st}, raw
}

func seedRepoTrigger(t *testing.T, st *store.Store, id, repo string) {
	t.Helper()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: id, Pipeline: "deploy", Repo: repo, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTrigger %s: %v", id, err)
	}
}

func seedSecret(t *testing.T, st *store.Store, name, value, repo string, shared bool) {
	t.Helper()
	if err := st.CreateOrReplaceSecret(store.Secret{
		Name: name, Value: value, Principal: "root", Repo: repo, Masked: true, Shared: shared,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("CreateOrReplaceSecret %s/%s: %v", name, repo, err)
	}
}

// The whole controller conversation a trigger-handling runner has, in
// order. Every step runs on the documented runner scope set, so a route
// that regains an `admin` requirement fails this test rather than the
// operator's first pipeline.
func TestRunnerScopes_DocumentedSetCompletesARunWithoutAdmin(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)

	trig, err := c.ClaimTrigger(ctx)
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if trig == nil {
		t.Fatal("ClaimTrigger returned no trigger; the queue was seeded with one")
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: trig.ClaimSeq})
	gotTrigger, err := c.GetTrigger(ctx, trig.ID)
	if err != nil {
		t.Fatalf("GetTrigger while holding the trigger claim: %v", err)
	}
	if gotTrigger.ID != trig.ID {
		t.Fatalf("GetTrigger ID = %q, want %q", gotTrigger.ID, trig.ID)
	}
	if _, err := c.HeartbeatTrigger(ctx, trig.ID); err != nil {
		t.Fatalf("HeartbeatTrigger: %v", err)
	}

	absentIsFine := func(err error) error {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}

	for _, step := range []struct {
		name string
		call func() error
	}{
		{"read the run for execution, the node process's first call", func() error {
			_, err := c.GetRunForExecution(ctx, trig.ID)
			return absentIsFine(err)
		}},
		{"read the trigger, the node process's second call", func() error {
			_, err := c.GetTrigger(ctx, trig.ID)
			return absentIsFine(err)
		}},
		{"create the run", func() error {
			return c.CreateRun(ctx, store.Run{
				ID: trig.ID, Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
			})
		}},
		{"re-read the run it just created", func() error {
			run, err := c.GetRunForExecution(ctx, trig.ID)
			if err != nil {
				return err
			}
			if run == nil || run.Pipeline != "deploy" {
				return errors.New("GetRunForExecution returned no run for the claimed trigger")
			}
			return nil
		}},
		{"persist the plan snapshot", func() error {
			return c.UpdatePlanSnapshot(ctx, trig.ID, []byte(`{"nodes":[]}`))
		}},
		{"create the build node", func() error {
			return c.CreateNode(ctx, store.Node{RunID: trig.ID, NodeID: "build", Status: "pending"})
		}},
		{"create the deploy node", func() error {
			return c.CreateNode(ctx, store.Node{RunID: trig.ID, NodeID: "deploy", Status: "pending"})
		}},
		{"expand dependencies", func() error {
			return c.UpdateNodeDeps(ctx, trig.ID, "deploy", []string{"build"})
		}},
		{"append a run event", func() error {
			return c.AppendEvent(ctx, trig.ID, "build", "node_queued", []byte(`{}`))
		}},
		{"heartbeat the run", func() error { return c.TouchRunHeartbeat(ctx, trig.ID) }},
		{"start the node", func() error { return c.StartNode(ctx, trig.ID, "build") }},
		{"annotate the node", func() error { return c.AppendNodeAnnotation(ctx, trig.ID, "build", "compiling") }},
		{"pause the node", func() error {
			return c.SetNodeStatus(ctx, trig.ID, "build", "paused")
		}},
		{"resume the node", func() error {
			return c.SetNodeStatus(ctx, trig.ID, "build", "running")
		}},
		{"finish the node", func() error {
			return c.FinishNode(ctx, trig.ID, "build", "success", "", nil)
		}},
		{"finish the run", func() error { return c.FinishRun(ctx, trig.ID, "success", "") }},
		{"finish the trigger", func() error { return c.FinishTrigger(ctx, trig.ID) }},
	} {
		if err := step.call(); err != nil {
			t.Fatalf("%s on the documented runner scope set: %v", step.name, err)
		}
	}

	run, err := f.store.GetRun(ctx, trig.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "success" {
		t.Errorf("run status = %q, want success", run.Status)
	}
	if run.Repo != "acme/web" {
		t.Errorf("run repo = %q, want the trigger's acme/web", run.Repo)
	}
}

func TestTriggerClaimMutation_RequiresExactTokenAndGeneration(t *testing.T) {
	f, ownerRaw := newScopedFixture(t, runnerScopes)
	otherRaw, _, err := f.store.CreateToken("other-pool", store.TokenKindRunner,
		runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken other-pool: %v", err)
	}
	owner := client.NewWithToken(f.url, nil, ownerRaw)
	other := client.NewWithToken(f.url, nil, otherRaw)
	ctx := context.Background()

	seedRepoTrigger(t, f.store, "heartbeat-trigger", "acme/web")
	claimed, err := owner.ClaimTrigger(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimTrigger heartbeat-trigger = (%+v, %v)", claimed, err)
	}
	claimCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq})
	if _, err := owner.HeartbeatTrigger(claimCtx, claimed.ID); err != nil {
		t.Fatalf("owner heartbeat: %v", err)
	}
	if _, err := other.HeartbeatTrigger(claimCtx, claimed.ID); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("other-token heartbeat = %v, want ErrLockHeld", err)
	}
	if _, err := f.store.DB().Exec(`UPDATE triggers SET claim_seq = claim_seq + 1 WHERE id = ?`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.HeartbeatTrigger(claimCtx, claimed.ID); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("stale-generation heartbeat = %v, want ErrLockHeld", err)
	}
	currentCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq + 1})
	if _, err := owner.HeartbeatTrigger(currentCtx, claimed.ID); err != nil {
		t.Fatalf("current-generation heartbeat: %v", err)
	}

	seedRepoTrigger(t, f.store, "finish-trigger", "acme/web")
	claimed, err = owner.ClaimTrigger(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimTrigger finish-trigger = (%+v, %v)", claimed, err)
	}
	claimCtx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq})
	if err := other.FinishTrigger(claimCtx, claimed.ID); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("other-token finish = %v, want ErrLockHeld", err)
	}
	if err := owner.FinishTrigger(claimCtx, claimed.ID); err != nil {
		t.Fatalf("owner finish: %v", err)
	}
	if err := owner.FinishTrigger(claimCtx, claimed.ID); err != nil {
		t.Fatalf("owner finish retry after a lost response: %v", err)
	}
	got, err := f.store.GetTrigger(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if got.Status != "done" {
		t.Fatalf("trigger status = %q, want done", got.Status)
	}
}

func TestRunnerScopes_TriggerOwnedExecutionAttemptRoundTrips(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)
	seedRepoTrigger(t, f.store, "trigger-attempt", "acme/web")
	trigger, err := c.ClaimTrigger(ctx)
	if err != nil || trigger == nil {
		t.Fatalf("ClaimTrigger = (%+v, %v)", trigger, err)
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: trigger.ClaimSeq})
	if err := c.CreateRun(ctx, store.Run{
		ID: trigger.ID, Pipeline: trigger.Pipeline, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateNode(ctx, store.Node{RunID: trigger.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := c.StartNode(ctx, trigger.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if err := c.AcknowledgeNodeExecutionStart(ctx, trigger.ID, "build", store.ExecutionStart{
		ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatalf("AcknowledgeNodeExecutionStart: %v", err)
	}
	if err := c.FinishNodeExecutionAttempt(ctx, trigger.ID, "build", store.ExecutionAttemptFinish{
		ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: store.FailureVerify,
	}); err != nil {
		t.Fatalf("FinishNodeExecutionAttempt: %v", err)
	}
	node, err := f.store.GetNode(context.Background(), trigger.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.AttemptsConsumed != 1 || len(node.ExecutionAttempts) != 1 ||
		node.ExecutionAttempts[0].Outcome != "failed" {
		t.Fatalf("stored trigger attempt = %+v", node)
	}
}

func TestRunMutationFence_RechecksAfterRequestBodyUnblocks(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)
	seedRepoTrigger(t, f.store, "slow-finish", "acme/web")
	trigger, err := c.ClaimTrigger(ctx)
	if err != nil || trigger == nil {
		t.Fatalf("ClaimTrigger = (%+v, %v)", trigger, err)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: trigger.ID, Pipeline: "deploy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	reader, writer := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, f.url+"/api/v1/runs/"+trigger.ID+"/finish", reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.TriggerGenerationHeader, fmt.Sprint(trigger.ClaimSeq))
	response := make(chan *http.Response, 1)
	requestErr := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			requestErr <- err
			return
		}
		response <- resp
	}()
	if _, err := writer.Write([]byte(`{"status":"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().Exec(`UPDATE triggers SET lease_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixNano(), trigger.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(`success"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-requestErr:
		t.Fatal(err)
	case resp := <-response:
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("finish status = %d body %q, want 409", resp.StatusCode, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked finish request did not return")
	}
	run, err := f.store.GetRun(ctx, trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" {
		t.Fatalf("run status = %q, want running after rejected stale finish", run.Status)
	}
}

func TestRunnerScopes_PoolRunnerReadsItsRepositorySecret(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)
	seedRepoTrigger(t, f.store, "run-web", "acme/web")
	seedRunNode(t, f.store, "run-web", "build")
	setRunRepo(t, f.store, "run-web", "acme/web")
	if err := f.store.MarkNodeReady(ctx, "run-web", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if _, err := c.GetRunForExecution(ctx, "run-web"); err != nil {
		t.Fatalf("GetRunForExecution while holding the node claim: %v", err)
	}
	if _, err := c.GetTrigger(ctx, "run-web"); err != nil {
		t.Fatalf("GetTrigger while holding the node claim: %v", err)
	}

	sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", "run-web")
	if err != nil {
		t.Fatalf("GetSecretForRun while holding the claim: %v", err)
	}
	if sec.Value != "web-key" {
		t.Errorf("GetSecretForRun value = %q, want the claimed run's repository row", sec.Value)
	}
}

func setRunRepo(t *testing.T, st *store.Store, runID, repo string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE runs SET repo = ? WHERE id = ?`, repo, runID); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerScopes_SecretsReadIsNotAdmin(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()

	seedSecret(t, f.store, "DEPLOY_KEY", "api-key", "acme/api", false)
	seedRunNode(t, f.store, "run-web", "build")
	setRunRepo(t, f.store, "run-web", "acme/web")
	if err := f.store.MarkNodeReady(ctx, "run-web", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewWithToken(f.url, nil, raw).
		ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	t.Run("cannot read another repository's secret", func(t *testing.T) {
		c := client.NewWithToken(f.url, nil, raw)
		if _, err := c.GetSecretForRepo(ctx, "DEPLOY_KEY", "acme/api"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetSecretForRepo(acme/api) err = %v, want ErrNotFound; the caller named a repo it does not hold", err)
		}
	})

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"mint a token", http.MethodPost, "/api/v1/tokens", `{"principal":"mallory","kind":"user","scopes":["admin"]}`},
		{"list tokens", http.MethodGet, "/api/v1/tokens", ""},
		{"read users", http.MethodGet, "/api/v1/users", ""},
		{"create a user", http.MethodPost, "/api/v1/users", `{"name":"mallory","password":"hunter2hunter2"}`},
		{"list every secret", http.MethodGet, "/api/v1/secrets", ""},
		{"write a secret", http.MethodPost, "/api/v1/secrets", `{"name":"X","value":"y"}`},
		{"mark a node ready", http.MethodPost, "/api/v1/runs/run-web/nodes/build/mark-ready", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, raw, tc.method, tc.path, tc.body); got != http.StatusForbidden {
				t.Errorf("%s %s = %d, want 403", tc.method, tc.path, got)
			}
		})
	}
}

// A runner cannot repoint its own run at another repository and then
// read that repository's credential.
func TestRunnerScopes_RunRepositoryComesFromTheTrigger(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-web", "acme/web")
	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)
	seedSecret(t, f.store, "DEPLOY_KEY", "api-key", "acme/api", false)

	trig, err := c.ClaimTrigger(ctx)
	if err != nil || trig == nil {
		t.Fatalf("ClaimTrigger: %v (trigger %v)", err, trig)
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: trig.ClaimSeq})
	if err := c.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := c.FinishRun(ctx, "run-web", "pending", ""); err != nil {
		t.Fatalf("FinishRun(pending): %v", err)
	}
	if err := c.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy", Status: "running",
		Repo: "acme/api", StartedAt: time.Now().UTC(),
	}); err == nil {
		t.Error("CreateRun with a repo the trigger does not name succeeded, want a 400")
	}
	if err := c.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun replay: %v", err)
	}

	run, err := f.store.GetRun(ctx, "run-web")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Repo != "acme/web" {
		t.Fatalf("run repo = %q, want acme/web; the runner rewrote it", run.Repo)
	}
	sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", "run-web")
	if err != nil {
		t.Fatalf("GetSecretForRun: %v", err)
	}
	if sec.Value != "web-key" {
		t.Errorf("GetSecretForRun value = %q, want web-key", sec.Value)
	}
}

// One pool token holds claims in two repositories at once, so the read
// has to name the run it is for instead of picking one.
func TestRunnerScopes_SecretReadNamesItsRun(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "acme/web", false)
	seedSecret(t, f.store, "DEPLOY_KEY", "api-key", "acme/api", false)
	for _, seed := range []struct{ run, repo string }{
		{"run-web", "acme/web"},
		{"run-api", "acme/api"},
	} {
		seedRunNode(t, f.store, seed.run, "build")
		setRunRepo(t, f.store, seed.run, seed.repo)
		if err := f.store.MarkNodeReady(ctx, seed.run, "build"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ClaimNode(ctx, "holder-"+seed.run, nil, time.Minute, nil); err != nil {
			t.Fatalf("ClaimNode %s: %v", seed.run, err)
		}
	}

	if _, err := c.GetSecret(ctx, "DEPLOY_KEY"); err == nil {
		t.Error("GetSecret with claims in two repositories succeeded, want a refusal naming ?run")
	}
	for _, tc := range []struct{ run, want string }{
		{"run-web", "web-key"},
		{"run-api", "api-key"},
	} {
		sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", tc.run)
		if err != nil {
			t.Fatalf("GetSecretForRun(%s): %v", tc.run, err)
		}
		if sec.Value != tc.want {
			t.Errorf("GetSecretForRun(%s) = %q, want %q", tc.run, sec.Value, tc.want)
		}
	}
}

func TestRunnerScopes_UnscopedSecretNeedsShared(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "LEGACY_KEY", "legacy", "", false)
	seedSecret(t, f.store, "NPM_TOKEN", "npm", "", true)
	seedRunNode(t, f.store, "run-web", "build")
	setRunRepo(t, f.store, "run-web", "acme/web")
	if err := f.store.MarkNodeReady(ctx, "run-web", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	if _, err := c.GetSecretForRun(ctx, "LEGACY_KEY", "run-web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSecretForRun(LEGACY_KEY) err = %v, want ErrNotFound until an admin shares it", err)
	}
	sec, err := c.GetSecretForRun(ctx, "NPM_TOKEN", "run-web")
	if err != nil {
		t.Fatalf("GetSecretForRun(NPM_TOKEN): %v", err)
	}
	if sec.Value != "npm" {
		t.Errorf("GetSecretForRun(NPM_TOKEN) = %q, want npm", sec.Value)
	}
	if got := f.do(t, raw, http.MethodGet, "/api/v1/secrets/LEGACY_KEY", ""); got != http.StatusNotFound {
		t.Errorf("GET LEGACY_KEY = %d, want 404", got)
	}
}

func TestRunnerScopes_StateWritesStillNeedTheNodeClaim(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()

	stranger, _, err := f.store.CreateToken("pool-b", store.TokenKindRunner,
		runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	seedRunNode(t, f.store, "run-1", "build")
	if err := f.store.MarkNodeReady(ctx, "run-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewWithToken(f.url, nil, raw).
		ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	for _, path := range []string{
		"/api/v1/runs/run-1?include=secret_values",
		"/api/v1/triggers/run-1",
	} {
		if got := f.do(t, stranger, http.MethodGet, path, ""); got != http.StatusForbidden {
			t.Errorf("stranger GET %s = %d, want 403", path, got)
		}
	}

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"start a node", "/api/v1/runs/run-1/nodes/build/start", `{}`},
		{"finish a node", "/api/v1/runs/run-1/nodes/build/finish", `{"outcome":"success"}`},
		{"finish the run", "/api/v1/runs/run-1/finish", `{"status":"success"}`},
		{"forge an event", "/api/v1/runs/run-1/events", `{"node_id":"build","kind":"node_failed"}`},
		{"inject a node", "/api/v1/runs/run-1/nodes", `{"run_id":"run-1","id":"backdoor","status":"pending"}`},
		{"rewrite the plan", "/api/v1/runs/run-1/plan", `{}`},
		{"upsert the run", "/api/v1/runs", `{"id":"run-1","pipeline":"demo","status":"running"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, stranger, http.MethodPost, tc.path, tc.body); got != http.StatusForbidden {
				t.Errorf("stranger POST %s = %d, want 403", tc.path, got)
			}
		})
	}
}

// The two reads a node process starts with are admitted by the claim,
// not by the scope, so a runner token holding no claim on the run still
// gets nothing.
func TestRunnerScopes_RunAndTriggerReadsStillNeedTheClaim(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	if _, err := client.NewWithToken(f.url, nil, raw).ClaimTrigger(ctx); err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	stranger, _, err := f.store.CreateToken("pool-b", store.TokenKindRunner,
		runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}

	for _, tc := range []struct{ name, path string }{
		{"read the run", "/api/v1/runs/run-1"},
		{"read the trigger", "/api/v1/triggers/run-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, stranger, http.MethodGet, tc.path, ""); got != http.StatusForbidden {
				t.Errorf("stranger GET %s = %d, want 403", tc.path, got)
			}
		})
	}
}

// A pin outlives the run that writes it, so the route is bound to a
// live claim on a run of that pipeline rather than to the scope alone.
func TestRunnerScopes_ProfilePinNeedsAClaimOnThatPipeline(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRunNode(t, f.store, "run-1", "build")
	if err := f.store.MarkNodeReady(ctx, "run-1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if err := f.store.RecordProfileObservation(ctx, "demo", "build", store.ProfileObservation{
		Duration: time.Minute, PeakCores: 3, PeakMemoryBytes: 4 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.SetPipelinePin(ctx, "demo", "build", 3, 4<<30); err != nil {
		t.Fatalf("SetPipelinePin on the pipeline this runner is executing: %v", err)
	}
	prof, err := f.store.GetPipelineProfile(ctx, "demo", "build")
	if err != nil || prof == nil {
		t.Fatalf("GetPipelineProfile: %v (profile %v)", err, prof)
	}
	if prof.PinnedCores != 3 {
		t.Errorf("pinned cores = %v, want 3", prof.PinnedCores)
	}

	if err := c.SetPipelinePin(ctx, "release", "build", 1, 1<<30); err == nil {
		t.Error("SetPipelinePin on a pipeline this runner holds no claim in succeeded, want 403")
	}

	stranger, _, err := f.store.CreateToken("pool-b", store.TokenKindRunner,
		runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	if err := client.NewWithToken(f.url, nil, stranger).
		SetPipelinePin(ctx, "demo", "build", 1, 1<<30); err == nil {
		t.Error("a runner token holding no claim pinned another runner's pipeline, want 403")
	}
}

func TestRunnerScopes_ClaimScopedReadsExpireAndKeepSecretsNodeOnly(t *testing.T) {
	t.Run("trigger claim", func(t *testing.T) {
		f, raw := newScopedFixture(t, []string{controller.ScopeTriggersClaim})
		ctx := context.Background()
		c := client.NewWithToken(f.url, nil, raw)

		seedRepoTrigger(t, f.store, "run-trigger", "acme/web")
		if err := f.store.CreateRun(ctx, store.Run{
			ID:       "run-trigger",
			Pipeline: "deploy",
			Status:   "pending",
			Args:     map[string]string{"token": "trigger-secret"},
			Invocation: map[string]any{
				store.InvocationSecretArgsKey: []string{"token"},
			},
			StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		trig, err := c.ClaimTrigger(ctx)
		if err != nil || trig == nil {
			t.Fatalf("ClaimTrigger: %v (trigger %v)", err, trig)
		}
		run, err := c.GetRunForExecution(ctx, trig.ID)
		if err != nil {
			t.Fatalf("GetRunForExecution with live trigger claim: %v", err)
		}
		if got := run.Args["token"]; got != "***" {
			t.Fatalf("trigger claimant secret arg = %q, want redacted value", got)
		}

		if _, err := f.store.DB().ExecContext(ctx,
			`UPDATE triggers SET lease_expires_at = ? WHERE id = ?`,
			time.Now().Add(-time.Minute).UnixNano(), trig.ID); err != nil {
			t.Fatalf("expire trigger claim: %v", err)
		}
		for _, path := range []string{
			"/api/v1/runs/run-trigger?include=secret_values",
			"/api/v1/triggers/run-trigger",
		} {
			if got := f.do(t, raw, http.MethodGet, path, ""); got != http.StatusForbidden {
				t.Errorf("expired trigger claimant GET %s = %d, want 403", path, got)
			}
		}
	})

	t.Run("node claim", func(t *testing.T) {
		f, raw := newScopedFixture(t, []string{controller.ScopeNodesClaim})
		ctx := context.Background()
		c := client.NewWithToken(f.url, nil, raw)

		seedRepoTrigger(t, f.store, "run-node", "acme/web")
		if err := f.store.CreateRun(ctx, store.Run{
			ID:       "run-node",
			Pipeline: "deploy",
			Status:   "running",
			Args:     map[string]string{"token": "node-secret"},
			Invocation: map[string]any{
				store.InvocationSecretArgsKey: []string{"token"},
			},
			StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := f.store.CreateNode(ctx, store.Node{
			RunID: "run-node", NodeID: "build", Status: "pending",
		}); err != nil {
			t.Fatalf("CreateNode: %v", err)
		}
		if err := f.store.MarkNodeReady(ctx, "run-node", "build"); err != nil {
			t.Fatalf("MarkNodeReady: %v", err)
		}
		if _, err := c.ClaimNode(ctx, "holder-node", nil, time.Minute, nil); err != nil {
			t.Fatalf("ClaimNode: %v", err)
		}
		run, err := c.GetRunForExecution(ctx, "run-node")
		if err != nil {
			t.Fatalf("GetRunForExecution with live node claim: %v", err)
		}
		if got := run.Args["token"]; got != "node-secret" {
			t.Fatalf("node claimant secret arg = %q, want plaintext", got)
		}
		if _, err := c.GetTrigger(ctx, "run-node"); err != nil {
			t.Fatalf("GetTrigger with live node claim: %v", err)
		}

		if _, err := f.store.DB().ExecContext(ctx,
			`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`,
			time.Now().Add(-time.Minute).UnixNano(), "run-node", "build"); err != nil {
			t.Fatalf("expire node claim: %v", err)
		}
		for _, path := range []string{
			"/api/v1/runs/run-node?include=secret_values",
			"/api/v1/triggers/run-node",
		} {
			if got := f.do(t, raw, http.MethodGet, path, ""); got != http.StatusForbidden {
				t.Errorf("expired node claimant GET %s = %d, want 403", path, got)
			}
		}
	})
}

func (f scopedFixture) do(t *testing.T, token, method, path, body string) int {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// The coordination conversation an orchestrator process has: it claims a
// named trigger, records the queue wait before any node of the run
// exists, drives its child triggers, and folds the run's measurements at
// the end. Every step runs on the documented runner scope set and on the
// trigger claim alone, so a route that needs a node claim or a wider
// scope fails here rather than on a laptop.
func TestRunnerScopes_CoordinationRoutesRunOnATriggerClaim(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: "child-1", Pipeline: "child", ParentRunID: "run-1", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed the child trigger: %v", err)
	}

	claimed, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}
	parentCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq})
	var childCtx context.Context

	for _, step := range []struct {
		name string
		call func() error
	}{
		{"record the admission wait, before any node of the run exists", func() error {
			return c.RecordWaitObservation(ctx, "deploy", 250*time.Millisecond)
		}},
		{"create the run the claim names", func() error {
			return c.CreateRun(parentCtx, store.Run{
				ID: "run-1", Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
			})
		}},
		{"create the build node", func() error {
			return c.CreateNode(parentCtx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"})
		}},
		{"list the run's own nodes", func() error {
			nodes, err := c.ListNodes(parentCtx, "run-1")
			if err != nil {
				return err
			}
			if len(nodes) != 1 {
				return errors.New("ListNodes returned no nodes for the claimed run")
			}
			return nil
		}},
		{"list the children this run spawned", func() error {
			ids, err := c.ListPendingTriggersForParent(parentCtx, "run-1")
			if err != nil {
				return err
			}
			if len(ids) != 1 || ids[0] != "child-1" {
				return errors.New("ListPendingTriggersForParent did not return the seeded child")
			}
			return nil
		}},
		{"claim the child trigger by id", func() error {
			child, err := c.ClaimSpecificTrigger(parentCtx, "child-1", time.Minute)
			if err == nil {
				childCtx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: child.ClaimSeq})
			}
			return err
		}},
		{"create the child run the new claim names", func() error {
			return c.CreateRun(childCtx, store.Run{
				ID: "child-1", Pipeline: "child", Status: "running", StartedAt: time.Now().UTC(),
			})
		}},
		{"create a node on the child run", func() error {
			return c.CreateNode(childCtx, store.Node{RunID: "child-1", NodeID: "build", Status: "pending"})
		}},
		{"sample the node's resources", func() error {
			return c.AddNodeMetricSample(parentCtx, "run-1", "build", store.MetricSample{
				TS: time.Now().UTC(), CPUMillicores: 500, MemoryBytes: 1 << 20,
			})
		}},
		{"read the samples back at run end", func() error {
			samples, err := c.ListNodeMetrics(parentCtx, "run-1", "build")
			if err != nil {
				return err
			}
			if len(samples) != 1 {
				return errors.New("ListNodeMetrics returned no samples for the claimed run")
			}
			return nil
		}},
		{"fold the reaped process's accounting into the node", func() error {
			return c.AddNodeUsage(parentCtx, "run-1", "build", store.NodeUsage{
				CPUTime: time.Second, MaxRSSBytes: 1 << 20, Wall: 2 * time.Second,
			})
		}},
		{"fold the run's measurement into the profile", func() error {
			return c.RecordProfileObservation(ctx, "deploy", "", store.ProfileObservation{
				Duration: time.Minute, PeakCores: 2, SustainedCores: 1, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
			})
		}},
		{"record that the run was contended", func() error { return c.RecordContention(ctx, "deploy") }},
		{"pin what the run was charged", func() error {
			return c.SetPipelinePin(ctx, "deploy", "", 2, 1<<30)
		}},
		{"finish the child trigger", func() error { return c.FinishTrigger(childCtx, "child-1") }},
		{"finish the run", func() error { return c.FinishRun(parentCtx, "run-1", "success", "") }},
		{"finish the trigger", func() error { return c.FinishTrigger(parentCtx, "run-1") }},
	} {
		if err := step.call(); err != nil {
			t.Fatalf("%s on the documented runner scope set: %v", step.name, err)
		}
	}

	prof, err := f.store.GetPipelineProfile(ctx, "deploy", "")
	if err != nil || prof == nil {
		t.Fatalf("GetPipelineProfile: %v (profile %v)", err, prof)
	}
	if prof.PinnedCores != 2 {
		t.Errorf("pinned cores = %v, want 2", prof.PinnedCores)
	}
	node, err := f.store.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.MaxRSSBytes != 1<<20 {
		t.Errorf("node max rss = %d, want the usage the runner folded in", node.MaxRSSBytes)
	}
}

// A claim on one pipeline is not standing on another, and a token holding
// no claim at all has none anywhere.
func TestRunnerScopes_CoordinationWritesNeedAClaimOnThatPipeline(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	if _, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}

	if err := c.RecordWaitObservation(ctx, "release", time.Second); err == nil {
		t.Error("recorded a wait against a pipeline this runner holds no claim in, want 403")
	}
	if err := c.RecordProfileObservation(ctx, "release", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 1, CPUMeasured: true,
	}); err == nil {
		t.Error("recorded an observation against a pipeline this runner holds no claim in, want 403")
	}
	if err := c.RecordContention(ctx, "release"); err == nil {
		t.Error("recorded contention against a pipeline this runner holds no claim in, want 403")
	}

	stranger, _, err := f.store.CreateToken("pool-b", store.TokenKindRunner, runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	sc := client.NewWithToken(f.url, nil, stranger)
	if err := sc.RecordContention(ctx, "deploy"); err == nil {
		t.Error("a runner holding no claim recorded contention on another runner's pipeline, want 403")
	}
	if _, err := sc.ListPendingTriggersForParent(ctx, "run-1"); err == nil {
		t.Error("a runner holding no claim listed another run's pending children, want 403")
	}
	if _, err := sc.ListNodeMetrics(ctx, "run-1", "build"); err == nil {
		t.Error("a runner holding no claim read another run's node metrics, want 403")
	}
}

// A capacity profile is scoped by repository, so a claim on one
// repository's pipeline is no standing on another repository's pipeline
// of the same name. The pin the write can set is a hard limit for every
// later run of the pipeline it names.
func TestRunnerScopes_ProfileWritesAreScopedToTheClaimedRepository(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	if _, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}

	own := store.JoinProfileKey("github.com/acme/web", "deploy")
	other := store.JoinProfileKey("github.com/evil/other", "deploy")
	measurement := store.ProfileObservation{
		Duration: time.Minute, PeakCores: 99, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}

	if err := c.RecordProfileObservation(ctx, own, "", measurement); err != nil {
		t.Fatalf("observation on this runner's own repository: %v", err)
	}
	if err := c.SetPipelinePin(ctx, own, "", 64, 1<<30); err != nil {
		t.Fatalf("pin on this runner's own repository: %v", err)
	}
	if err := c.RecordProfileObservation(ctx, "deploy", "", measurement); err != nil {
		t.Fatalf("observation on the unscoped key: %v", err)
	}

	if err := c.RecordProfileObservation(ctx, other, "", measurement); err == nil {
		t.Error("wrote another repository's profile for a pipeline of the same name")
	}
	if err := c.SetPipelinePin(ctx, other, "", 64, 1<<30); err == nil {
		t.Error("pinned another repository's pipeline of the same name")
	}
	if err := c.RecordContention(ctx, other); err == nil {
		t.Error("recorded contention on another repository's pipeline")
	}
	if prof, err := f.store.GetPipelineProfile(ctx, other, ""); err != nil || prof != nil {
		t.Errorf("other-repository profile = %v (err %v), want none: every write to it was refused", prof, err)
	}
	if prof, err := f.store.GetPipelineProfile(ctx, own, ""); err != nil || prof == nil || prof.PinnedCores != 64 {
		t.Fatalf("own profile = %v (err %v), want a row pinned at 64 cores", prof, err)
	}
}

// A pin is a hard limit, so the route bounds it the way the observation
// routes bound their readings.
func TestRunnerScopes_PinRefusesAnUnboundedFigure(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	if _, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}

	if err := c.SetPipelinePin(ctx, "deploy", "", -5, 1<<30); err == nil {
		t.Error("a negative core pin was accepted; it is neither a clear nor a limit")
	}
	if err := c.SetPipelinePin(ctx, "deploy", "", 1e9, 1<<30); err == nil {
		t.Error("a billion-core pin was accepted")
	}
	prof, err := f.store.GetPipelineProfile(ctx, "deploy", "")
	if err != nil {
		t.Fatalf("GetPipelineProfile: %v", err)
	}
	if prof != nil {
		t.Errorf("profile = %+v, want none: every pin was refused", prof)
	}
}

// A trigger returned to the queue carries no claimant, so the principal
// that held it before cannot ride a later in-process claim.
func TestRunnerScopes_AReleasedTriggerDropsItsClaimant(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedRepoTrigger(t, f.store, "run-1", "acme/web")
	if _, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}
	if _, err := f.store.RequeueUnstartedClaim(ctx, "run-1"); err != nil {
		t.Fatalf("RequeueUnstartedClaim: %v", err)
	}

	var principal, prefix string
	if err := f.store.DB().QueryRowContext(ctx,
		`SELECT claim_principal, claim_token_prefix FROM triggers WHERE id = ?`, "run-1",
	).Scan(&principal, &prefix); err != nil {
		t.Fatalf("read the released row: %v", err)
	}
	if principal != "" || prefix != "" {
		t.Errorf("released trigger still names %q/%q as its claimant", principal, prefix)
	}

	if _, err := f.store.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatalf("in-process re-claim: %v", err)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "deploy", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"read the trigger", func() error { _, err := c.GetTrigger(ctx, "run-1"); return err }},
		{"create a node on the live run", func() error {
			return c.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "intruder", Status: "pending"})
		}},
		{"finish the live run", func() error { return c.FinishRun(ctx, "run-1", "failed", "") }},
		{"record contention against its pipeline", func() error { return c.RecordContention(ctx, "deploy") }},
	} {
		if err := tc.call(); err == nil {
			t.Errorf("the released principal could still %s", tc.name)
		}
	}

	run, err := f.store.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("run status = %q, want running: a principal holding nothing finished it", run.Status)
	}
}
