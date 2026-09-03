package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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

func TestAgents_DerivedFromClaims(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	now := time.Now()

	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-a",
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "n2", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	_ = st.MarkNodeReady(ctx, "run-a", "n1")
	_ = st.MarkNodeReady(ctx, "run-a", "n2")
	if _, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "runner:laptop-alice:1", 30*time.Second, nil); err != nil {
		t.Fatalf("claim1: %v", err)
	}
	if _, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, "pod:run-a:n-pool", 30*time.Second, nil); err != nil {
		t.Fatalf("claim2: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	data, err := httpGet(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Agents []controller.Agent `json:"agents"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, data)
	}
	if len(body.Agents) < 2 {
		t.Fatalf("expected 2 agents, got %d: %+v", len(body.Agents), body.Agents)
	}
	var sawAgent, sawPool bool
	for _, a := range body.Agents {
		if a.Type == "agent" && a.Name == "laptop-alice" {
			sawAgent = true
			if a.Status != "busy" || a.Location != "unknown" || a.ActiveSlots != nil {
				t.Errorf("laptop-alice = %+v, want busy at unknown location", a)
			}
		}
		if a.Type == "pool" && a.Name == "run-a" {
			sawPool = true
		}
	}
	if !sawAgent {
		t.Errorf("missing laptop-alice agent: %+v", body.Agents)
	}
	if !sawPool {
		t.Errorf("missing pool pod: %+v", body.Agents)
	}
}

func TestAgents_RegisteredIdleAndOfflineExecutorsExposeNoPrincipal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	for _, executor := range []store.Executor{
		{Name: "idle-agent", Kind: "agent", Location: "local", Capabilities: []string{"linux"}, MaxConcurrent: 2, Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 8 << 30}, Headroom: store.ExecutorResource{Cores: 3, MemoryBytes: 6 << 30}, LastSeen: time.Now()},
		{Name: "old-gateway", Kind: "gateway", Location: "cloud", MaxConcurrent: 4, Headroom: store.ExecutorResource{Cores: 8}, LastSeen: time.Now().Add(-3 * time.Minute)},
	} {
		prefix := "swr_" + executor.Name
		executor.Principal = "runner-secret-principal"
		if err := st.EnrollExecutor(ctx, prefix, executor); err != nil {
			t.Fatalf("EnrollExecutor(%s): %v", executor.Name, err)
		}
		if err := st.HeartbeatExecutor(ctx,
			store.ClaimIdentity{Principal: executor.Principal, TokenPrefix: prefix}, executor.Name,
			executor.Headroom, 0, executor.LastSeen); err != nil {
			t.Fatalf("HeartbeatExecutor(%s): %v", executor.Name, err)
		}
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	data, err := httpGet(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "runner-secret-principal") || strings.Contains(string(data), "swr_") || strings.Contains(string(data), "token_prefix") {
		t.Fatalf("agent response exposed a principal or token prefix: %s", data)
	}
	var body struct {
		Agents []controller.Agent `json:"agents"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 2 {
		t.Fatalf("agents = %+v, want two registered executors", body.Agents)
	}
	byName := map[string]controller.Agent{}
	for _, agent := range body.Agents {
		byName[agent.Name] = agent
	}
	idle := byName["idle-agent"]
	if idle.Status != "idle" || idle.Location != "local" || idle.MaxConcurrent != 2 || idle.Headroom == nil || idle.Budget.Cores != 4 {
		t.Fatalf("idle executor = %+v", idle)
	}
	offline := byName["old-gateway"]
	if offline.Status != "offline" || offline.Type != "gateway" || offline.Location != "cloud" || offline.Headroom != nil {
		t.Fatalf("offline executor = %+v", offline)
	}
}

func TestAgents_AdminEnrollmentExactCredentialLivenessAndLegacyBoundary(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	adminRaw, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	runnerRaw, runner, err := st.CreateToken("same-principal", store.TokenKindRunner, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	rotatedRaw, rotated, err := st.CreateToken("same-principal", store.TokenKindRunner, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	enrollment := map[string]any{
		"token_prefix": runner.Prefix, "kind": "agent", "location": "local",
		"capabilities": []string{"linux", "trusted"}, "base_priority": 10,
		"priority_ceiling": 30, "max_concurrent": 2,
		"budget": map[string]any{"cores": 4, "memory_bytes": 8 << 30},
	}
	if status, body := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/desk", adminRaw, enrollment); status != http.StatusNoContent {
		t.Fatalf("admin enrollment = %d: %s", status, body)
	}
	if status, body := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/other", adminRaw, enrollment); status != http.StatusConflict {
		t.Fatalf("duplicate credential enrollment = %d: %s", status, body)
	} else if strings.Contains(body, runner.Prefix) {
		t.Fatalf("duplicate credential response exposed token prefix: %s", body)
	}
	stored, err := st.ExecutorForCredential(context.Background(),
		store.ClaimIdentity{Principal: "ignored", TokenPrefix: runner.Prefix}, "desk")
	if err != nil || stored.Principal != "same-principal" {
		t.Fatalf("enrollment principal = %q, %v; want token owner", stored.Principal, err)
	}
	if status, body := agentRequest(t, http.MethodPost, srv.URL+"/api/v1/agents/desk/heartbeat", runnerRaw,
		map[string]any{"headroom": map[string]any{"cores": 3, "memory_bytes": 6 << 30, "queue_depth": 1}}); status != http.StatusNoContent {
		t.Fatalf("heartbeat = %d: %s", status, body)
	}
	if status, body := agentRequest(t, http.MethodPost, srv.URL+"/api/v1/agents/desk/heartbeat", rotatedRaw,
		map[string]any{"headroom": map[string]any{"cores": 99, "memory_bytes": 0, "queue_depth": 0}}); status != http.StatusConflict {
		t.Fatalf("same-principal different-token heartbeat = %d, want 409", status)
	} else if body != "{\"error\":\"executor credential does not match enrollment\"}\n" || strings.Contains(body, rotated.Prefix) {
		t.Fatalf("credential mismatch body = %q", body)
	}
	if status, _ := agentRequest(t, http.MethodPost, srv.URL+"/api/v1/agents/desk/heartbeat", runnerRaw,
		map[string]any{"headroom": map[string]any{"cores": -1, "memory_bytes": 0, "queue_depth": 0}}); status != http.StatusBadRequest {
		t.Fatalf("invalid heartbeat = %d, want 400", status)
	}
	if status, _ := agentRequest(t, http.MethodPost, srv.URL+"/api/v1/agents/desk/heartbeat", runnerRaw,
		map[string]any{"headroom": map[string]any{"cores": 3, "memory_bytes": 0, "queue_depth": 0}, "location": "cloud"}); status != http.StatusBadRequest {
		t.Fatalf("heartbeat location self-report = %d, want 400", status)
	}
	spoofed := maps.Clone(enrollment)
	spoofed["capabilities"] = []string{"linux", "location=cloud"}
	if status, _ := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/spoofed", adminRaw, spoofed); status != http.StatusBadRequest {
		t.Fatalf("reserved capability enrollment = %d, want 400", status)
	}

	status, body := agentRequest(t, http.MethodGet, srv.URL+"/api/v1/agents", adminRaw, nil)
	if status != http.StatusOK {
		t.Fatalf("list agents = %d: %s", status, body)
	}
	if strings.Contains(body, runner.Prefix) || strings.Contains(body, rotated.Prefix) || strings.Contains(body, "same-principal") || strings.Contains(body, "token_prefix") {
		t.Fatalf("agents response exposed credential metadata: %s", body)
	}
	var listed struct {
		Agents []controller.Agent `json:"agents"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil || len(listed.Agents) != 1 {
		t.Fatalf("agents response = %+v, %v", listed, err)
	}
	got := listed.Agents[0]
	if got.Name != "desk" || got.Headroom == nil || got.Headroom.Cores != 3 || got.Budget.Cores != 4 || got.BasePriority != 10 || got.PriorityCeiling != 30 {
		t.Fatalf("registered agent = %+v", got)
	}

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "work"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "work-2", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "work-2"); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "same-principal", TokenPrefix: runner.Prefix}
	for slot, nodeID := range []string{"work", "work-2"} {
		summary, err := st.SchedulingSummary(ctx, "run-1", nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimReadyNodeForExecutorWithReservation(ctx, claimant, "desk", "run-1", nodeID,
			fmt.Sprintf("runner:desk:%d", slot), time.Minute, fmt.Sprintf("reservation-%d", slot), slot, summary.ResourceDigest); err != nil {
			t.Fatalf("claim %s: %v", nodeID, err)
		}
	}
	status, body = agentRequest(t, http.MethodGet, srv.URL+"/api/v1/agents", adminRaw, nil)
	if status != http.StatusOK {
		t.Fatalf("list active agent = %d: %s", status, body)
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil || len(listed.Agents) != 1 {
		t.Fatalf("active agents response = %+v, %v", listed, err)
	}
	got = listed.Agents[0]
	if got.ActiveSlots == nil || *got.ActiveSlots != 2 || len(got.ActiveJobs) != 1 || got.ActiveJobs[0] != "run-1" {
		t.Fatalf("registered activity = %+v, want two slots in one active job", got)
	}
	if _, err := client.NewWithToken(srv.URL, nil, runnerRaw).ClaimNode(ctx, "agent:desk:1", nil, time.Minute, nil); err == nil {
		t.Fatal("enrolled credential fell back to legacy /nodes/claim")
	} else if strings.Contains(err.Error(), runner.Prefix) || strings.Contains(err.Error(), "same-principal") {
		t.Fatalf("legacy claim rejection exposed credential identity: %v", err)
	}

	enrollment["token_prefix"] = rotated.Prefix
	if status, body := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/desk", adminRaw, enrollment); status != http.StatusNoContent {
		t.Fatalf("rotate enrollment = %d: %s", status, body)
	}
	status, body = agentRequest(t, http.MethodGet, srv.URL+"/api/v1/agents", adminRaw, nil)
	if status != http.StatusOK {
		t.Fatalf("list rotated agent = %d: %s", status, body)
	}
	var rotatedList struct {
		Agents []controller.Agent `json:"agents"`
	}
	if err := json.Unmarshal([]byte(body), &rotatedList); err != nil || len(rotatedList.Agents) != 1 {
		t.Fatalf("rotated agents response = %+v, %v", rotatedList, err)
	}
	if rotatedList.Agents[0].Status != "offline" || rotatedList.Agents[0].Headroom != nil {
		t.Fatalf("rotated enrollment retained old credential liveness: %+v", rotatedList.Agents[0])
	}
	if status, body := agentRequest(t, http.MethodPost, srv.URL+"/api/v1/agents/desk/heartbeat", runnerRaw,
		map[string]any{"headroom": map[string]any{"cores": 1, "memory_bytes": 0, "queue_depth": 0}}); status != http.StatusConflict {
		t.Fatalf("old credential after rotation = %d, want 409", status)
	} else if body != "{\"error\":\"executor credential does not match enrollment\"}\n" || strings.Contains(body, runner.Prefix) {
		t.Fatalf("rotated credential mismatch body = %q", body)
	}
	if status, body := agentRequest(t, http.MethodPost, srv.URL+"/api/v1/agents/desk/heartbeat", rotatedRaw,
		map[string]any{"headroom": map[string]any{"cores": 2, "memory_bytes": 0, "queue_depth": 0}}); status != http.StatusNoContent {
		t.Fatalf("rotated credential heartbeat = %d: %s", status, body)
	}
	if err := st.RevokeToken(rotated.Prefix, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if status, _ := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/desk", adminRaw, enrollment); status != http.StatusBadRequest {
		t.Fatalf("enrollment accepted revoked prefix: status %d", status)
	}
	enrollment["token_prefix"] = "swr_missing"
	if status, _ := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/desk", adminRaw, enrollment); status != http.StatusBadRequest {
		t.Fatalf("enrollment accepted missing prefix: status %d", status)
	}
}

func TestAgents_EnrollmentLimitReturnsStableError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for i := range store.MaxEnrolledExecutors {
		name := fmt.Sprintf("seed-%d", i)
		if err := st.EnrollExecutor(ctx, "swr_"+name, store.Executor{
			Name: name, Kind: "agent", Location: "local", Principal: "principal-" + name, MaxConcurrent: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	adminRaw, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	_, runner, err := st.CreateToken("runner", store.TokenKindRunner, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()
	status, body := agentRequest(t, http.MethodPut, srv.URL+"/api/v1/agents/overflow", adminRaw, map[string]any{
		"token_prefix": runner.Prefix, "kind": "agent", "location": "local", "max_concurrent": 1,
		"budget": map[string]any{"cores": 0, "memory_bytes": 0},
	})
	if status != http.StatusConflict {
		t.Fatalf("enrollment status = %d: %s", status, body)
	}
	want := `"error":"` + store.ErrExecutorEnrollmentLimit.Error() + `"`
	if !strings.Contains(body, want) {
		t.Fatalf("enrollment limit response = %s, want %s", body, want)
	}
}

func agentRequest(t *testing.T, method, target, token string, body any) (int, string) {
	t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, target, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func TestAgents_EmptyWhenNoClaims(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	data, err := httpGet(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Agents []controller.Agent `json:"agents"`
	}
	_ = json.Unmarshal(data, &body)
	if len(body.Agents) != 0 {
		t.Fatalf("expected 0 agents, got %d", len(body.Agents))
	}
}
