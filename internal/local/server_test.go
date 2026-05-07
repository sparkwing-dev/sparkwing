package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	controller "github.com/sparkwing-dev/sparkwing/v2/internal/local"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// newTestServer spins up an httptest.Server backed by a fresh SQLite
// file. Caller gets the base URL + the store handle (for assertions)
// and a cleanup closure.
func newTestServer(t *testing.T) (baseURL string, st *store.Store, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctrl := controller.New(s, nil)
	srv := httptest.NewServer(ctrl.Handler())
	return srv.URL, s, func() {
		srv.Close()
		_ = s.Close()
	}
}

func TestController_Health(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	resp := mustGet(t, base+"/api/v1/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status=%q want ok", body["status"])
	}
}

func TestController_RunLifecycle(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()

	// 1. CreateRun.
	run := store.Run{
		ID:        "run-ctrl-1",
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}
	mustPostJSON(t, base+"/api/v1/runs", run, http.StatusCreated)

	// 2. UpdatePlanSnapshot (raw bytes body).
	snapshot := []byte(`{"nodes":[{"id":"a"},{"id":"b"}]}`)
	mustPostRaw(t, base+"/api/v1/runs/run-ctrl-1/plan", snapshot, http.StatusNoContent)

	// 3. CreateNode twice.
	mustPostJSON(t, base+"/api/v1/runs/run-ctrl-1/nodes",
		store.Node{NodeID: "a", Status: "pending"},
		http.StatusCreated)
	mustPostJSON(t, base+"/api/v1/runs/run-ctrl-1/nodes",
		store.Node{NodeID: "b", Status: "pending", Deps: []string{"a"}},
		http.StatusCreated)

	// 4. StartNode + AppendEvent + FinishNode for "a".
	mustPost(t, base+"/api/v1/runs/run-ctrl-1/nodes/a/start", http.StatusNoContent)
	mustPostJSON(t, base+"/api/v1/runs/run-ctrl-1/events",
		map[string]any{"node_id": "a", "kind": "node_started"},
		http.StatusOK)
	mustPostJSON(t, base+"/api/v1/runs/run-ctrl-1/nodes/a/finish",
		map[string]any{"outcome": "success"},
		http.StatusNoContent)

	// 5. UpdateNodeDeps on "b" (simulate an expansion backfill).
	mustPostJSON(t, base+"/api/v1/runs/run-ctrl-1/nodes/b/deps",
		map[string]any{"deps": []string{"a", "dyn-1", "dyn-2"}},
		http.StatusNoContent)

	// 6. FinishRun.
	mustPostJSON(t, base+"/api/v1/runs/run-ctrl-1/finish",
		map[string]any{"status": "success"},
		http.StatusNoContent)

	// --- verify via the underlying store ---
	got, err := st.GetRun(context.Background(), "run-ctrl-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("run status=%q want success", got.Status)
	}
	if !bytes.Equal(got.PlanSnapshot, snapshot) {
		t.Errorf("plan snapshot mismatch:\n got %s\nwant %s", got.PlanSnapshot, snapshot)
	}

	nodes, err := st.ListNodes(context.Background(), "run-ctrl-1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes=%d want 2", len(nodes))
	}
	// Node "b" should carry the expanded deps list.
	var bNode *store.Node
	for _, n := range nodes {
		if n.NodeID == "b" {
			bNode = n
			break
		}
	}
	if bNode == nil {
		t.Fatalf("node b not found")
	}
	if len(bNode.Deps) != 3 || bNode.Deps[2] != "dyn-2" {
		t.Errorf("node b deps=%v want [a dyn-1 dyn-2]", bNode.Deps)
	}
}

// GET /api/v1/runs/{id} returns raw store.Run by default, but with
// ?include=nodes wraps the response as {run, nodes}. Both shapes
// must keep working: the dashboard consumes the wrapped form, the
// CLI + cluster runner consume the unwrapped one.
func TestController_GetRun_IncludeNodes(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	mustPostJSON(t, base+"/api/v1/runs", store.Run{
		ID: "run-incl", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}, http.StatusCreated)
	mustPostJSON(t, base+"/api/v1/runs/run-incl/nodes",
		store.Node{NodeID: "a", Status: "pending"}, http.StatusCreated)
	mustPostJSON(t, base+"/api/v1/runs/run-incl/nodes",
		store.Node{NodeID: "b", Status: "pending", Deps: []string{"a"}},
		http.StatusCreated)

	// Default shape: raw store.Run, no wrapper.
	resp := mustGet(t, base+"/api/v1/runs/run-incl")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default get status=%d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode default: %v", err)
	}
	if raw["id"] != "run-incl" {
		t.Errorf("default shape: id=%v want run-incl (run not at top level)", raw["id"])
	}
	if _, hasRun := raw["run"]; hasRun {
		t.Errorf("default shape leaked the {run:...} wrapper: %v", raw)
	}

	// include=nodes shape: {run, nodes}.
	resp = mustGet(t, base+"/api/v1/runs/run-incl?include=nodes")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("include get status=%d", resp.StatusCode)
	}
	var wrapped map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		t.Fatalf("decode wrapped: %v", err)
	}
	run, ok := wrapped["run"].(map[string]any)
	if !ok {
		t.Fatalf("wrapped.run missing or wrong type: %v", wrapped)
	}
	if run["id"] != "run-incl" {
		t.Errorf("wrapped.run.id=%v want run-incl", run["id"])
	}
	nodes, ok := wrapped["nodes"].([]any)
	if !ok {
		t.Fatalf("wrapped.nodes missing or wrong type: %v", wrapped)
	}
	if len(nodes) != 2 {
		t.Errorf("wrapped.nodes len=%d want 2", len(nodes))
	}
}

// When the run carries a plan snapshot, GET /api/v1/runs/{id}?include=nodes
// attaches per-node decorations (modifiers, groups, approval,
// on_failure_of, dynamic, inner-Work tree) under a nested
// `decorations` object. Nodes without snapshot adornments emit no
// `decorations` key.
func TestController_GetRun_IncludeNodes_Decorations(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	mustPostJSON(t, base+"/api/v1/runs", store.Run{
		ID: "run-deco", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}, http.StatusCreated)

	// PlanSnapshot covers every decoration: modifiers, group,
	// approval, on_failure_of, dynamic, inner-Work tree.
	snapshot := []byte(`{
  "nodes": [
    {"id": "build", "groups": ["ci"],
     "modifiers": {"retry": 2, "timeout_ms": 30000},
     "work": {"steps": [{"id": "compile"}]}},
    {"id": "release", "approval": {"message": "ship?"}},
    {"id": "rollback", "on_failure_of": "release"},
    {"id": "expand", "dynamic": true},
    {"id": "plain"}
  ]
}`)
	mustPostRaw(t, base+"/api/v1/runs/run-deco/plan", snapshot, http.StatusNoContent)

	for _, id := range []string{"build", "release", "rollback", "expand", "plain"} {
		mustPostJSON(t, base+"/api/v1/runs/run-deco/nodes",
			store.Node{NodeID: id, Status: "pending"}, http.StatusCreated)
	}

	resp := mustGet(t, base+"/api/v1/runs/run-deco?include=nodes")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("include get status=%d", resp.StatusCode)
	}
	var wrapped struct {
		Run   map[string]any   `json:"run"`
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, n := range wrapped.Nodes {
		byID[n["id"].(string)] = n
	}

	// build: modifiers + groups + work
	build := byID["build"]
	if build == nil {
		t.Fatal("missing build node")
	}
	bd, ok := build["decorations"].(map[string]any)
	if !ok {
		t.Fatalf("build.decorations missing: %v", build)
	}
	groups, _ := bd["groups"].([]any)
	if len(groups) != 1 || groups[0] != "ci" {
		t.Errorf("build.decorations.groups=%v want [ci]", bd["groups"])
	}
	mods, _ := bd["modifiers"].(map[string]any)
	if mods == nil || mods["retry"].(float64) != 2 || mods["timeout_ms"].(float64) != 30000 {
		t.Errorf("build.decorations.modifiers=%v want retry=2 timeout_ms=30000", bd["modifiers"])
	}
	if _, ok := bd["work"].(map[string]any); !ok {
		t.Errorf("build.decorations.work missing: %v", bd)
	}

	// release: approval pill
	if rd, ok := byID["release"]["decorations"].(map[string]any); !ok || rd["approval"] != true {
		t.Errorf("release.decorations.approval=%v want true (entry %v)", rd["approval"], byID["release"])
	}

	// rollback: on_failure_of
	if rd, ok := byID["rollback"]["decorations"].(map[string]any); !ok || rd["on_failure_of"] != "release" {
		t.Errorf("rollback.decorations.on_failure_of=%v want release", rd["on_failure_of"])
	}

	// expand: dynamic
	if rd, ok := byID["expand"]["decorations"].(map[string]any); !ok || rd["dynamic"] != true {
		t.Errorf("expand.decorations.dynamic=%v want true", rd["dynamic"])
	}

	// plain: no decorations key (omitempty)
	if _, ok := byID["plain"]["decorations"]; ok {
		t.Errorf("plain node leaked decorations key: %v", byID["plain"])
	}
}

// With no PlanSnapshot, every wrapped node marshals as bare
// store.Node fields and no `decorations` key appears anywhere in
// the response.
func TestController_GetRun_IncludeNodes_NoSnapshot(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	mustPostJSON(t, base+"/api/v1/runs", store.Run{
		ID: "run-bare", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}, http.StatusCreated)
	mustPostJSON(t, base+"/api/v1/runs/run-bare/nodes",
		store.Node{NodeID: "a", Status: "pending"}, http.StatusCreated)

	resp := mustGet(t, base+"/api/v1/runs/run-bare?include=nodes")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("include get status=%d", resp.StatusCode)
	}
	var wrapped struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(wrapped.Nodes) != 1 {
		t.Fatalf("nodes=%d want 1", len(wrapped.Nodes))
	}
	if _, ok := wrapped.Nodes[0]["decorations"]; ok {
		t.Errorf("no-snapshot run leaked decorations key: %v", wrapped.Nodes[0])
	}
	if wrapped.Nodes[0]["id"] != "a" {
		t.Errorf("id=%v want a", wrapped.Nodes[0]["id"])
	}
}

// The dashboard SPA reads debug pauses via /api/v1/runs/{id}/paused,
// an alias of GET /api/v1/runs/{id}/debug-pauses. Both routes must
// return identical shapes.
func TestController_ListPausesAlias(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	mustPostJSON(t, base+"/api/v1/runs", store.Run{
		ID: "run-pause", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}, http.StatusCreated)

	// Empty list response (no pauses created): both routes return [].
	resp := mustGet(t, base+"/api/v1/runs/run-pause/paused")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("paused alias status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp = mustGet(t, base+"/api/v1/runs/run-pause/debug-pauses")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("debug-pauses status=%d", resp.StatusCode)
	}
	canonicalBody, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, canonicalBody) {
		t.Errorf("alias body diverges from canonical:\n  alias:    %s\n  canonical: %s",
			body, canonicalBody)
	}
}

func TestController_ValidationErrors(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	// CreateRun requires id/pipeline/status.
	mustPostJSON(t, base+"/api/v1/runs",
		map[string]any{"pipeline": "only-pipeline"},
		http.StatusBadRequest)

	// FinishRun requires status.
	mustPostJSON(t, base+"/api/v1/runs/none/finish",
		map[string]any{},
		http.StatusBadRequest)
}

// --- test helpers ---

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func mustPost(t *testing.T, url string, wantStatus int) {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status=%d want %d (body: %s)", url, resp.StatusCode, wantStatus, body)
	}
}

func mustPostJSON(t *testing.T, url string, body any, wantStatus int) {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		rbody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status=%d want %d (body: %s)", url, resp.StatusCode, wantStatus, rbody)
	}
}

func mustPostRaw(t *testing.T, url string, body []byte, wantStatus int) {
	t.Helper()
	resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		rbody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status=%d want %d (body: %s)", url, resp.StatusCode, wantStatus, rbody)
	}
}
