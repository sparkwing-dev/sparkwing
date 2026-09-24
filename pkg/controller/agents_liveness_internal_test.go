package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestBusyLegacyRunnerHeartbeatUpdatesObservedLivenessWithoutPolling(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	raw, identity, err := st.CreateToken("runner", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run", Pipeline: "p", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run", "work"); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: identity.Principal, TokenPrefix: identity.Prefix}
	holder := "moonborn:1790249256598651000"
	node, err := st.ClaimNextReadyNode(ctx, claimant, holder, time.Minute, nil)
	if err != nil || node == nil {
		t.Fatalf("claim = %+v, %v", node, err)
	}
	old := now.Add(-2 * time.Minute)
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET started_at = ? WHERE run_id = ?`, old.UnixNano(), "run"); err != nil {
		t.Fatal(err)
	}
	s := New(st, nil).EnableAuthFromStore()
	key := presenceKey{tokenPrefix: identity.Prefix, name: "moonborn"}
	s.runnerPresence.record(key, []string{"local"}, &claimCapacity{MaxConcurrent: 1, ActiveClaims: 1}, nil, old)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	beat, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/runs/run/nodes/work/heartbeat",
		bytes.NewBufferString(`{"holder_id":"`+holder+`","lease_secs":60}`))
	if err != nil {
		t.Fatal(err)
	}
	beat.Header.Set("Authorization", "Bearer "+raw)
	beat.Header.Set("Content-Type", "application/json")
	beat.Header.Set(store.ClaimHolderHeader, holder)
	beat.Header.Set(store.ClaimMembershipHeader, node.ClaimMembershipID)
	beat.Header.Set(store.ClaimReservationHeader, node.ReservationID)
	beat.Header.Set(store.ClaimGenerationHeader, strconv.FormatInt(node.ClaimGeneration, 10))
	response, err := http.DefaultClient.Do(beat)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", response.StatusCode)
	}
	if live := s.runnerPresence.live(time.Now(), 20*time.Second, presenceKey{}); len(live) != 0 {
		t.Fatalf("heartbeat offered stale poll capacity: %+v", live)
	}
	acceptedAt, ok := s.runnerHeartbeats.lookup(key, time.Now())
	if !ok {
		t.Fatal("accepted heartbeat was not recorded")
	}
	rejected, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/runs/run/nodes/work/heartbeat",
		bytes.NewBufferString(`{"holder_id":"`+holder+`","lease_secs":60}`))
	if err != nil {
		t.Fatal(err)
	}
	rejected.Header = beat.Header.Clone()
	rejected.Header.Set(store.ClaimGenerationHeader, strconv.FormatInt(node.ClaimGeneration+1, 10))
	rejection, err := http.DefaultClient.Do(rejected)
	if err != nil {
		t.Fatal(err)
	}
	rejection.Body.Close()
	if rejection.StatusCode != http.StatusConflict {
		t.Fatalf("wrong fence heartbeat = %d", rejection.StatusCode)
	}
	if seen, _ := s.runnerHeartbeats.lookup(key, time.Now()); !seen.Equal(acceptedAt) {
		t.Fatalf("rejected heartbeat refreshed liveness: %v -> %v", acceptedAt, seen)
	}
	rec := httptest.NewRecorder()
	s.handleAgents(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agents = %d: %s", rec.Code, rec.Body.String())
	}
	var answer struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || len(answer.Agents) != 1 {
		t.Fatalf("agents = %+v, %v", answer, err)
	}
	seen, err := time.Parse(time.RFC3339, answer.Agents[0].LastSeen)
	if err != nil || seen.Before(now.Add(-5*time.Second)) {
		t.Fatalf("busy runner last_seen = %q, want accepted heartbeat time", answer.Agents[0].LastSeen)
	}
	if answer.Agents[0].Status != "busy" {
		t.Fatalf("busy runner = %+v", answer.Agents[0])
	}
}
