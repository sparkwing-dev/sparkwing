package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestAgentsLogsInternalStoreFailure(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.DB().Exec(`DROP TABLE nodes`); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	srv := httptest.NewServer(controller.New(st, logger).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError || string(body) != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("response = %d %s", resp.StatusCode, body)
	}
	if got := logs.String(); !strings.Contains(got, "list agent claims") ||
		!strings.Contains(got, "no such table: nodes") ||
		!strings.Contains(got, `"method":"GET"`) ||
		!strings.Contains(got, `"route":"/api/v1/agents"`) {
		t.Fatalf("internal error log = %s", got)
	}
}

func TestAgents_DerivedFromClaims(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "s.db"))
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
			if a.Status != "busy" || a.Location != "unknown" {
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

func TestAgents_PlainHolderUsesOwnLivePoll(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	owner, ownerToken, err := st.CreateToken("agent-a", store.TokenKindRunner, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := st.CreateToken("agent-b", store.TokenKindRunner, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "old-run", Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "old-run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "old-run", "work"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(func() { srv.Close() })
	claim := func(token, holder string) int {
		status, body := agentRequest(t, http.MethodPost, srv.URL+"/api/v1/nodes/claim", token,
			map[string]any{
				"holder_id": holder, "labels": []string{"host=moonborn"},
				"capacity": map[string]any{"max_concurrent": 2, "active_claims": 0},
				"headroom": map[string]any{"cores": 2, "memory_bytes": 4 << 30, "queue_depth": 0},
			})
		if status != http.StatusOK && status != http.StatusNoContent {
			t.Fatalf("claim = %d: %s", status, body)
		}
		return status
	}
	if got := claim(owner, "moonborn:1790249256598651000"); got != http.StatusOK {
		t.Fatalf("first claim = %d", got)
	}
	old := now.Add(-10 * time.Minute)
	if _, err := st.DB().Exec(`UPDATE nodes SET status = 'done', started_at = ?, lease_expires_at = ? WHERE run_id = 'old-run'`, old.UnixNano(), old.UnixNano()); err != nil {
		t.Fatal(err)
	}
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tenant.ListAgentClaims(ctx, now.Add(-time.Hour))
	if err != nil || len(claims) != 1 || claims[0].TokenPrefix == "" || claims[0].TokenPrefix != ownerToken.Prefix {
		t.Fatalf("stored claim credential matches owner = %t, owner length=%d, claim length=%d, err=%v", len(claims) == 1 && claims[0].TokenPrefix == ownerToken.Prefix, len(ownerToken.Prefix), len(claims[0].TokenPrefix), err)
	}
	srv.Close()
	srv = httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	list := func() controller.Agent {
		status, body := agentRequest(t, http.MethodGet, srv.URL+"/api/v1/agents", admin, nil)
		if status != http.StatusOK {
			t.Fatalf("agents = %d: %s", status, body)
		}
		var response struct {
			Agents []controller.Agent `json:"agents"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || len(response.Agents) != 1 {
			t.Fatalf("agents = %+v, %v", response, err)
		}
		return response.Agents[0]
	}
	if got := claim(other, "moonborn:1790249256598651001"); got != http.StatusNoContent {
		t.Fatalf("other credential poll = %d", got)
	}
	stale := list()
	if stale.Name != "moonborn" || stale.Type != "agent" || stale.MaxConcurrent != 0 || stale.Headroom != nil {
		t.Fatalf("unrelated poll changed the agent identity or capacity: %+v", stale)
	}
	staleSeen, err := time.Parse(time.RFC3339, stale.LastSeen)
	if err != nil || !staleSeen.Before(now.Add(-5*time.Minute)) {
		t.Fatalf("unrelated poll refreshed stale claim: %q, %v", stale.LastSeen, err)
	}
	if got := claim(owner, "moonborn:1790249256598651002"); got != http.StatusNoContent {
		t.Fatalf("same credential poll = %d", got)
	}
	live := list()
	liveSeen, err := time.Parse(time.RFC3339, live.LastSeen)
	if err != nil || live.Name != "moonborn" || live.Type != "agent" ||
		live.MaxConcurrent != 2 || live.Headroom == nil || live.Headroom.Cores != 2 ||
		liveSeen.Before(now.Add(-5*time.Second)) {
		t.Fatalf("live agent = %+v, time error %v", live, err)
	}
	ready := func(id string) {
		if err := st.CreateRun(ctx, store.Run{ID: id, Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: id, NodeID: "work", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkNodeReady(ctx, id, "work"); err != nil {
			t.Fatal(err)
		}
	}
	ready("run-a")
	if got := claim(owner, "moonborn:1790249256598651003"); got != http.StatusOK {
		t.Fatalf("owner claim = %d", got)
	}
	ready("run-b")
	if got := claim(other, "moonborn:1790249256598651004"); got != http.StatusOK {
		t.Fatalf("other claim = %d", got)
	}
	if _, err := st.DB().Exec(`UPDATE nodes SET started_at = ? WHERE run_id = 'run-a'`, now.Add(-2*time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE nodes SET started_at = ? WHERE run_id = 'run-b'`, now.Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	takeover := list()
	if len(takeover.ActiveJobs) != 1 || takeover.ActiveJobs[0] != "run-b" {
		t.Fatalf("latest credential's active runs = %v, want only run-b", takeover.ActiveJobs)
	}
	claimedSeen, err := time.Parse(time.RFC3339, takeover.LastSeen)
	if err != nil || claimedSeen.After(time.Now()) {
		t.Fatalf("claim observed in the future: %q, %v", takeover.LastSeen, err)
	}
	first, err := st.GetNode(ctx, "run-a", "work")
	if err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "agent-a", TokenPrefix: ownerToken.Prefix}
	heartbeatCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: claimant, HolderID: first.ClaimedBy,
		MembershipID: first.ClaimMembershipID, ReservationID: first.ReservationID,
		ClaimGeneration: first.ClaimGeneration,
	})
	if err := st.HeartbeatNodeClaim(heartbeatCtx, "run-a", "work", claimant, first.ClaimedBy, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	renewed := list()
	if len(renewed.ActiveJobs) != 1 || renewed.ActiveJobs[0] != "run-b" {
		t.Fatalf("older credential's lease renewal changed display owner: %v", renewed.ActiveJobs)
	}
	renewedSeen, err := time.Parse(time.RFC3339, renewed.LastSeen)
	if err != nil || renewedSeen.After(time.Now()) {
		t.Fatalf("heartbeat observed in the future: %q, %v", renewed.LastSeen, err)
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
	st, err := teststore.Open(filepath.Join(dir, "s.db"))
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
