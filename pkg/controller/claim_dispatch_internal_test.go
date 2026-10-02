package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type dispatchRouteFixture struct {
	st      *store.Store
	handler http.Handler
	reader  string
}

const dispatchHash = `"spec_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`

// safety: drives the production router, so these requests reach the routes
// the controller registers and the store functions they write through.
func newDispatchRouteFixture(t *testing.T) dispatchRouteFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "run-d", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlanNode(ctx, store.DefaultTeam, "run-d", now); err != nil {
		t.Fatal(err)
	}
	reader, _, err := st.CreateToken("reader", store.TokenKindRunner, []string{ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	return dispatchRouteFixture{st: st, handler: New(st, nil).EnableAuthFromStore().Handler(), reader: reader}
}

func (f dispatchRouteFixture) claim(t *testing.T, nodeID string, kind store.ClaimTokenKind) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	c, err := f.st.ClaimLaunch(ctx, store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
		store.LaunchClaimRequest{HolderID: "holder-" + nodeID, Lease: time.Minute, Deadline: time.Hour, RunID: "run-d", NodeID: nodeID}, now)
	if err != nil || c == nil || c.Kind != kind {
		t.Fatalf("claim %s as %s: %+v %v", nodeID, kind, c, err)
	}
	return c.Token
}

func (f dispatchRouteFixture) post(t *testing.T, path, bearer, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f dispatchRouteFixture) resultDigest(t *testing.T, nodeID string) string {
	t.Helper()
	var digest string
	if err := f.st.DB().QueryRow(`SELECT result_digest FROM claim_tokens WHERE run_id = 'run-d' AND node_id = ?`,
		nodeID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	return digest
}

func wantPost(t *testing.T, f dispatchRouteFixture, path, bearer, body string, want int, status string) {
	t.Helper()
	code, out := f.post(t, path, bearer, body)
	if code != want || (status != "" && out["status"] != status) {
		t.Fatalf("POST %s: %d %v, want %d %q", path, code, out, want, status)
	}
}

const (
	planPath    = "/api/v1/runs/run-d/plan"
	attemptPath = "/api/v1/runs/run-d/nodes/a/attempt"
)

func TestPlanRoute_AcceptsReplaysAndRefuses(t *testing.T) {
	f := newDispatchRouteFixture(t)
	raw := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	body := `{"nodes":[{"id":"a","deps":[],` + dispatchHash + `}]}`
	wantPost(t, f, planPath, raw, `{"nodes":[{"id":"a","deps":["a"],`+dispatchHash+`}]}`, http.StatusUnprocessableEntity, "")
	wantPost(t, f, planPath, raw, body, http.StatusOK, "accepted")
	wantPost(t, f, planPath, raw, body, http.StatusOK, "replayed")
	wantPost(t, f, planPath, raw, `{"nodes":[]}`, http.StatusConflict, "")
	wantPost(t, f, "/api/v1/runs/run-d/nodes/plan/attempt", raw, `{"outcome":"failed"}`, http.StatusConflict, "")

	work := f.claim(t, "a", store.ClaimTokenWork)
	wantPost(t, f, planPath, work, body, http.StatusForbidden, "")
	wantPost(t, f, attemptPath, work, `{"outcome":"success","extra":1}`, http.StatusBadRequest, "")
	ref, _ := json.Marshal(f.uploadOutput(t, "run-d", "a", work, []byte(`{"v":1}`)))
	wantPost(t, f, attemptPath, work, `{"outcome":"success","output":`+string(ref)+`}`, http.StatusOK, "recorded")
	wantPost(t, f, attemptPath, work, `{"outcome":"success","output":`+string(ref)+`}`, http.StatusOK, "replayed")
	wantPost(t, f, attemptPath, work, `{"outcome":"failed"}`, http.StatusConflict, "")
	run, err := f.st.GetRun(context.Background(), "run-d")
	if err != nil || run.Status != "success" {
		t.Fatalf("run = %v %v", run, err)
	}
}

// The plan path still serves the scoped snapshot write for every bearer that
// is not a claim token; only a claim token reaches plan acceptance.
func TestPlanRoute_KeepsTheSnapshotRouteForOtherBearers(t *testing.T) {
	f := newDispatchRouteFixture(t)
	code, out := f.post(t, planPath, f.reader, `{}`)
	if code != http.StatusForbidden || out["error"] != "missing_scope" {
		t.Fatalf("scoped bearer on the plan path: %d %v; want the snapshot route's scope refusal", code, out)
	}
}

// The result digest commits in the transaction that writes the result, so a
// failure after the writes began rolls both back and the same claim can
// still commit. A digest committed on its own would answer the retry as a
// replay or a conflict instead.
func TestPlanRoute_AFailedCommitLeavesNoDigest(t *testing.T) {
	f := newDispatchRouteFixture(t)
	ctx := context.Background()
	if err := f.st.CreateNode(ctx, store.Node{RunID: "run-d", NodeID: "zz", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	raw := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	colliding := `{"nodes":[{"id":"a","deps":[],` + dispatchHash + `},{"id":"zz","deps":[],` + dispatchHash + `}]}`
	if code, out := f.post(t, planPath, raw, colliding); code != http.StatusInternalServerError {
		t.Fatalf("colliding plan: %d %v", code, out)
	}
	if d := f.resultDigest(t, store.PlanNodeID); d != "" {
		t.Fatalf("a failed plan commit left digest %q", d)
	}
	if _, err := f.st.GetNode(ctx, "run-d", "a"); err == nil {
		t.Fatal("a failed plan commit left node a")
	}
	wantPost(t, f, planPath, raw, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`}]}`, http.StatusOK, "accepted")
}

func TestAttemptRoute_AFailedCommitLeavesNoDigest(t *testing.T) {
	f := newDispatchRouteFixture(t)
	ctx := context.Background()
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f, planPath, plan, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`},{"id":"b","deps":["a"],`+dispatchHash+`}]}`,
		http.StatusOK, "accepted")
	raw := f.claim(t, "a", store.ClaimTokenWork)
	if _, err := f.st.DB().Exec(`UPDATE nodes SET deps_json = 'not json' WHERE run_id = 'run-d' AND node_id = 'b'`); err != nil {
		t.Fatal(err)
	}
	if code, out := f.post(t, attemptPath, raw, `{"outcome":"success"}`); code != http.StatusInternalServerError {
		t.Fatalf("attempt whose settle fails: %d %v", code, out)
	}
	if d := f.resultDigest(t, "a"); d != "" {
		t.Fatalf("a failed attempt commit left digest %q", d)
	}
	if a, err := f.st.GetNode(ctx, "run-d", "a"); err != nil || a.Status == "done" || a.ClaimedBy == "" {
		t.Fatalf("a failed attempt commit moved a: %+v %v", a, err)
	}
	if attempts, err := f.st.ListNodeExecutionAttempts(ctx, "run-d", "a"); err != nil || len(attempts) != 0 {
		t.Fatalf("a failed attempt commit left %d attempt rows (%v)", len(attempts), err)
	}
	if _, err := f.st.DB().Exec(`UPDATE nodes SET deps_json = '["a"]' WHERE run_id = 'run-d' AND node_id = 'b'`); err != nil {
		t.Fatal(err)
	}
	wantPost(t, f, attemptPath, raw, `{"outcome":"success"}`, http.StatusOK, "recorded")
}
