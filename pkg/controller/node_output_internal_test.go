package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f dispatchRouteFixture) admin(t *testing.T) string {
	t.Helper()
	raw, _, err := f.st.CreateToken("admin-"+t.Name(), store.TokenKindService, []string{ScopeAdmin}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f dispatchRouteFixture) do(t *testing.T, method, path, bearer string, body []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f dispatchRouteFixture) uploadOutput(t *testing.T, runID, nodeID, bearer string, data []byte) store.OutputRef {
	t.Helper()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	body, _ := json.Marshal(map[string]any{"size": len(data), "sha256": sha})
	code, raw := f.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/nodes/"+nodeID+"/output-upload", bearer, body)
	if code != http.StatusOK {
		t.Fatalf("output-upload %s: %d %s", nodeID, code, raw)
	}
	var grant store.OutputUploadGrant
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if code, raw := f.do(t, http.MethodPut, grant.URL, "", data); code != http.StatusOK {
		t.Fatalf("PUT output %s: %d %s", nodeID, code, raw)
	}
	body, _ = json.Marshal(map[string]string{"upload_id": grant.UploadID})
	if code, raw := f.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/nodes/"+nodeID+"/output-commit", bearer, body); code != http.StatusNoContent {
		t.Fatalf("output-commit %s: %d %s", nodeID, code, raw)
	}
	return store.OutputRef{Key: grant.Key, Size: int64(len(data)), SHA256: sha}
}

func (f dispatchRouteFixture) report(t *testing.T, nodeID, bearer string, ref store.OutputRef) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"outcome": "success", "output": ref})
	wantPost(t, f, "/api/v1/runs/run-d/nodes/"+nodeID+"/attempt", bearer, string(body), http.StatusOK, "recorded")
}

func (f dispatchRouteFixture) readOutput(t *testing.T, path, bearer string) (int, []byte) {
	t.Helper()
	code, raw := f.do(t, http.MethodGet, path, bearer, nil)
	if code != http.StatusOK {
		return code, raw
	}
	var grant store.OutputReadGrant
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.URL == "" {
		return code, nil
	}
	code, data := f.do(t, http.MethodGet, grant.URL, "", nil)
	sum := sha256.Sum256(data)
	if code != http.StatusOK || hex.EncodeToString(sum[:]) != grant.SHA256 {
		t.Fatalf("GET %s: %d %s, want the bytes of digest %s", grant.URL, code, data, grant.SHA256)
	}
	return code, data
}

func TestClaimOutputs_ReadOnlyAncestorsOfItsOwnRun(t *testing.T) {
	f := newDispatchRouteFixture(t)
	ctx := context.Background()
	if err := f.st.CreateRun(ctx, store.Run{ID: "run-other", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateNode(ctx, store.Node{RunID: "run-other", NodeID: "a", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishNode(ctx, "run-other", "a", "success", "", []byte(`{"from":"other"}`)); err != nil {
		t.Fatal(err)
	}
	if code, data := f.readOutput(t, "/api/v1/runs/run-other/nodes/a/output", f.admin(t)); code != http.StatusOK || string(data) != `{"from":"other"}` {
		t.Fatalf("an operator reading run-other: %d %s", code, data)
	}
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f, planPath, plan, `{"nodes":[`+
		`{"id":"a","deps":[],`+dispatchHash+`},{"id":"c","deps":[],`+dispatchHash+`},`+
		`{"id":"b","deps":["a"],`+dispatchHash+`},{"id":"d","deps":["b"],`+dispatchHash+`}]}`, http.StatusOK, "accepted")

	a := f.claim(t, "a", store.ClaimTokenWork)
	wantPost(t, f, attemptPath, a, `{"outcome":"success","output":{"v":1}}`, http.StatusBadRequest, "")
	if code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/c/output-upload", a,
		[]byte(`{"size":1,"sha256":"`+strings.Repeat("a", 64)+`"}`)); code != http.StatusForbidden {
		t.Fatalf("a claim uploading another node's output: %d %s", code, raw)
	}
	f.report(t, "a", a, f.uploadOutput(t, "run-d", "a", a, []byte(`{"from":"a"}`)))
	c := f.claim(t, "c", store.ClaimTokenWork)
	f.report(t, "c", c, f.uploadOutput(t, "run-d", "c", c, []byte(`{"from":"c"}`)))

	b := f.claim(t, "b", store.ClaimTokenWork)
	if code, data := f.readOutput(t, "/api/v1/runs/run-d/nodes/a/output", b); code != http.StatusOK || string(data) != `{"from":"a"}` {
		t.Fatalf("b reading its dependency a: %d %s", code, data)
	}
	if code, raw := f.do(t, http.MethodGet, "/api/v1/runs/run-d/nodes/c/output", b, nil); code != http.StatusForbidden ||
		!strings.Contains(string(raw), "output_not_ancestor") {
		t.Fatalf("b reading c, which it does not depend on: %d %s", code, raw)
	}
	if code, raw := f.do(t, http.MethodGet, "/api/v1/runs/run-other/nodes/a/output", b, nil); code != http.StatusForbidden ||
		!strings.Contains(string(raw), "claim_mismatch") {
		t.Fatalf("b reading another run's output: %d %s", code, raw)
	}
	f.report(t, "b", b, f.uploadOutput(t, "run-d", "b", b, []byte(`{"from":"b"}`)))

	d := f.claim(t, "d", store.ClaimTokenWork)
	if code, data := f.readOutput(t, "/api/v1/runs/run-d/nodes/a/output", d); code != http.StatusOK || string(data) != `{"from":"a"}` {
		t.Fatalf("d reading its transitive dependency a: %d %s", code, data)
	}
	if code, data := f.readOutput(t, "/api/v1/runs/run-d/nodes/c/output", f.admin(t)); code != http.StatusOK || string(data) != `{"from":"c"}` {
		t.Fatalf("an operator reading any node of the run: %d %s", code, data)
	}
}

func TestOutputURLs_RefuseATamperedSignature(t *testing.T) {
	f := newDispatchRouteFixture(t)
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f, planPath, plan, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`}]}`, http.StatusOK, "accepted")
	a := f.claim(t, "a", store.ClaimTokenWork)
	f.report(t, "a", a, f.uploadOutput(t, "run-d", "a", a, []byte(`"x"`)))
	code, raw := f.do(t, http.MethodGet, "/api/v1/runs/run-d/nodes/a/output", f.admin(t), nil)
	if code != http.StatusOK {
		t.Fatalf("grant: %d %s", code, raw)
	}
	var grant store.OutputReadGrant
	_ = json.Unmarshal(raw, &grant)
	if code, _ := f.do(t, http.MethodGet, grant.URL, "", nil); code != http.StatusOK {
		t.Fatalf("the signed URL itself: %d", code)
	}
	tampered := strings.Replace(grant.URL, "sig=", "sig=0", 1)
	if code, _ := f.do(t, http.MethodGet, tampered, "", nil); code != http.StatusForbidden {
		t.Fatalf("a tampered signature: %d, want 403", code)
	}
	other := strings.Replace(grant.URL, "/outputs/run-d/a/", "/outputs/run-d/b/", 1)
	if code, _ := f.do(t, http.MethodGet, other, "", nil); code != http.StatusForbidden {
		t.Fatalf("a signature reused for another key: %d, want 403", code)
	}
}

func TestFinishRoute_RefusesInlineOutputBytes(t *testing.T) {
	f := newDispatchRouteFixture(t)
	admin := f.admin(t)
	if err := f.st.CreateNode(context.Background(), store.Node{RunID: "run-d", NodeID: "x", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/x/finish", admin,
		[]byte(`{"outcome":"success","output":"eyJ2IjoxfQ=="}`)); code != http.StatusBadRequest || !strings.Contains(string(raw), "OutputRef") {
		t.Fatalf("finish with inline output bytes: %d %s, want refused", code, raw)
	}
	sum := sha256.Sum256([]byte(`{"v":1}`))
	forged, _ := json.Marshal(map[string]any{"outcome": "success", "output": store.OutputRef{
		Key: "outputs/run-d/x/" + strings.Repeat("0", 32), Size: 7, SHA256: hex.EncodeToString(sum[:]),
	}})
	if code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/x/finish", admin, forged); code != http.StatusUnprocessableEntity {
		t.Fatalf("finish naming an uncommitted output: %d %s, want refused", code, raw)
	}
	ref := f.uploadOutput(t, "run-d", "x", admin, []byte(`{"v":1}`))
	body, _ := json.Marshal(map[string]any{"outcome": "success", "output": ref})
	if code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/x/finish", admin, body); code != http.StatusNoContent {
		t.Fatalf("finish naming its committed output: %d %s", code, raw)
	}
	if code, data := f.readOutput(t, "/api/v1/runs/run-d/nodes/x/output", admin); code != http.StatusOK || string(data) != `{"v":1}` {
		t.Fatalf("read back: %d %s", code, data)
	}
}

func TestPruneExpiredOutputs_DeletesBytesAndRowsPastRetention(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	old := time.Now().Add(-store.OutputRetention - time.Hour)
	for _, run := range []struct{ id, status string }{{"run-expired", "failed"}, {"run-pinned", "success"}} {
		if err := st.CreateRun(ctx, store.Run{ID: run.id, Pipeline: "p", Status: "running", StartedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: run.id, NodeID: "n", Status: "running"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNode(ctx, run.id, "n", "success", "", []byte(`"`+run.id+`"`)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().Exec(`UPDATE runs SET status = ?, finished_at = ? WHERE id = ?`, run.status, old.UnixNano(), run.id); err != nil {
			t.Fatal(err)
		}
	}
	expired, _ := st.GetNode(ctx, "run-expired", "n")
	path := store.OutputPath(st.OutputDir(), expired.OutputRef.Key)
	srv := New(st, nil)
	if err := srv.pruneExpiredOutputs(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an expired run's output file survived: %v", err)
	}
	if out, err := st.GetNodeOutput(ctx, "run-expired", "n"); err != nil || out != nil {
		t.Fatalf("an expired run's output still reads: %s, %v", out, err)
	}
	if out, err := st.GetNodeOutput(ctx, "run-pinned", "n"); err != nil || string(out) != `"run-pinned"` {
		t.Fatalf("the pipeline's newest success lost its output: %s, %v", out, err)
	}
}
