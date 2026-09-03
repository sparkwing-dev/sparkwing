package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRemoteExecutionChildEnvironmentDropsSupervisorAuthority(t *testing.T) {
	private := []string{
		"SPARKWING_AGENT_TOKEN=parent-token",
		remoteExecutionCapabilityEnv + "=stale-capability",
		remoteBrokeredClaimEnv + "=1",
		"SPARKWING_NODE_CLAIM_HOLDER=holder-secret",
		"SPARKWING_NODE_CLAIM_GENERATION=17",
		"SPARKWING_NODE_CLAIM_MEMBERSHIP=membership-secret",
		"SPARKWING_NODE_CLAIM_RESERVATION=reservation-secret",
		"SPARKWING_TRIGGER_CLAIM_GENERATION=9",
	}
	got, err := remoteExecutionChildEnvironment(append(private,
		"PATH=/safe/bin", "AWS_REGION=us-west-2", submissionEnvironmentAllowKey+"=AWS_REGION"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	for _, value := range private {
		name, _, _ := strings.Cut(value, "=")
		if strings.Contains(joined, name+"=") {
			t.Fatalf("private supervisor variable reached child: %s in %v", name, got)
		}
	}
	if !strings.Contains(joined, "PATH=/safe/bin") || !strings.Contains(joined, "AWS_REGION=us-west-2") {
		t.Fatalf("runtime or explicitly allowed environment was removed: %v", got)
	}
}

func TestRemoteExecutionChildEnvironmentDoesNotInheritHostCredentials(t *testing.T) {
	got, err := remoteExecutionChildEnvironment([]string{
		"PATH=/safe/bin", "HOME=/safe/home", "DATABASE_URL=postgres://user:password@db/sparkwing",
		"AWS_SECRET_ACCESS_KEY=host-secret", "DOCKER_HOST=tcp://host:2375", "CUSTOM=ambient",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	for _, sentinel := range []string{"password", "host-secret", "DOCKER_HOST", "CUSTOM"} {
		if strings.Contains(joined, sentinel) {
			t.Fatalf("host value %q reached child: %v", sentinel, got)
		}
	}
	if !strings.Contains(joined, "PATH=/safe/bin") || !strings.Contains(joined, "HOME=/safe/home") {
		t.Fatalf("minimal runtime environment missing: %v", got)
	}
}

func TestRemoteExecutionBrokerBindsExactAttemptAndDeniesSupervisorRoutes(t *testing.T) {
	type capturedRequest struct {
		path, authorization, holder, membership, reservation, generation string
		body                                                             map[string]any
	}
	var mu sync.Mutex
	var captured []capturedRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := capturedRequest{
			path: r.URL.Path, authorization: r.Header.Get("Authorization"),
			holder: r.Header.Get(store.ClaimHolderHeader), membership: r.Header.Get(store.ClaimMembershipHeader),
			reservation: r.Header.Get(store.ClaimReservationHeader), generation: r.Header.Get(store.ClaimGenerationHeader),
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&entry.body)
		}
		mu.Lock()
		captured = append(captured, entry)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	fence := store.NodeClaimFence{
		HolderID: "real-holder", MembershipID: "real-membership",
		ReservationID: "real-reservation", ClaimGeneration: 23,
	}
	broker, err := startRemoteExecutionBroker(upstream.URL, upstream.URL, "parent-token", "run-1", "node-a", fence, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()

	do := func(method, path, body, capability string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, broker.URL()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+capability)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.ClaimHolderHeader, "forged-holder")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for _, path := range []string{
		"/api/v1/nodes/claim",
		"/api/v1/runs/run-1/nodes/node-a/heartbeat",
		"/api/v1/triggers/run-1/heartbeat",
		"/api/v1/agents/desk/heartbeat",
		"/api/v1/tokens",
		"/api/v1/services",
		"/api/v1/concurrency/node-run-1-node-a/acquire",
		"/api/v1/secrets/DEPLOY_KEY?run=run-2",
	} {
		method := http.MethodPost
		if strings.HasPrefix(path, "/api/v1/secrets/") || path == "/api/v1/services" {
			method = http.MethodGet
		}
		if status := do(method, path, `{}`, broker.capability); status != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", path, status)
		}
	}
	if status := do(http.MethodGet, "/api/v1/secrets/DEPLOY_KEY?run=run-1", ``, broker.capability); status != http.StatusNoContent {
		t.Fatalf("exact run secret status = %d", status)
	}
	if status := do(http.MethodPost, "/api/v1/runs/run-1/nodes/node-a/execution-start",
		`{"holder_id":"forged","membership_id":"forged","reservation_id":"forged","claim_generation":99,"attempt_ordinal":2}`,
		broker.capability); status != http.StatusNoContent {
		t.Fatalf("execution start status = %d", status)
	}
	if status := do(http.MethodPost, "/api/v1/logs/run-1/node-a", `{}`, broker.capability); status != http.StatusNoContent {
		t.Fatalf("exact log status = %d", status)
	}
	if status := do(http.MethodPost, "/api/v1/logs/run-1/node-b", `{}`, broker.capability); status != http.StatusForbidden {
		t.Fatalf("other node log status = %d, want 403", status)
	}
	if status := do(http.MethodGet, "/api/v1/runs/run-1", ``, "wrong-capability"); status != http.StatusUnauthorized {
		t.Fatalf("wrong capability status = %d, want 401", status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 3 {
		t.Fatalf("upstream requests = %d, want only exact secret, start, and log: %+v", len(captured), captured)
	}
	start := captured[1]
	if start.authorization != "Bearer parent-token" || start.holder != fence.HolderID ||
		start.membership != fence.MembershipID || start.reservation != fence.ReservationID || start.generation != "23" {
		t.Fatalf("start was not rebound to supervisor authority: %+v", start)
	}
	if start.body["holder_id"] != fence.HolderID || start.body["membership_id"] != fence.MembershipID ||
		start.body["reservation_id"] != fence.ReservationID || start.body["claim_generation"] != float64(23) ||
		start.body["attempt_ordinal"] != float64(2) {
		t.Fatalf("attempt body = %#v", start.body)
	}
}

func TestRemoteExecutionBrokerProxiesOnlyContentAddressedArtifacts(t *testing.T) {
	artifact, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	broker, err := startRemoteExecutionBroker(upstream.URL, "", "parent-token", "run-1", "node-a", store.NodeClaimFence{}, artifact,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()

	body := "artifact-body"
	sum := sha256.Sum256([]byte(body))
	key := "artifacts/blobs/" + hex.EncodeToString(sum[:])
	do := func(method, path, value string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, broker.URL()+path, strings.NewReader(value))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+broker.capability)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := do(http.MethodPut, "/bin/"+key, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("content-addressed put = %d", resp.StatusCode)
	}
	resp = do(http.MethodGet, "/bin/"+key, "")
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != body {
		t.Fatalf("content-addressed get = %d %q", resp.StatusCode, got)
	}
	for _, tc := range []struct {
		path, value string
	}{
		{path: "/bin/arbitrary/key", value: body},
		{path: "/bin/artifacts/blobs/" + strings.Repeat("0", 64), value: body},
	} {
		resp = do(http.MethodPut, tc.path, tc.value)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("unsafe put %s = %d, want 400", tc.path, resp.StatusCode)
		}
	}
	if _, err := artifact.Get(context.Background(), fmt.Sprintf("artifacts/blobs/%064d", 0)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("mismatched digest stored: %v", err)
	}
}

func TestCollectDispatchEnvDropsRemoteExecutionAuthority(t *testing.T) {
	for name := range remoteExecutionPrivateEnv {
		t.Setenv(name, "sentinel-"+name)
	}
	node := buildNode(t, "deploy", &stubJob{}).
		Env("SPARKWING_NODE_CLAIM_HOLDER", "node-holder-sentinel")
	got := collectDispatchEnv(context.Background(), node, "run-7", nil)
	raw, err := json.Marshal(got.values)
	if err != nil {
		t.Fatal(err)
	}
	for name := range remoteExecutionPrivateEnv {
		if name == ArtifactStoreEnvVar {
			continue
		}
		if strings.Contains(string(raw), name) || strings.Contains(string(raw), "sentinel-"+name) {
			t.Fatalf("dispatch snapshot retained %s: %s", name, raw)
		}
	}
}

func TestClaimedRegisteredNodeRunsOnlyInIsolatedChild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	st, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-isolated", Pipeline: "registered-helper-pipeline", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()

	previous := runNodeIsolatedFn
	t.Cleanup(func() { runNodeIsolatedFn = previous })
	called := false
	runNodeIsolatedFn = func(_ context.Context, controllerURL, logsURL, runID, nodeID, token string, _ *slog.Logger) (runner.Result, error) {
		called = true
		if controllerURL != server.URL || logsURL != "" || runID != "run-isolated" || nodeID != "build" || token != "parent-token" {
			t.Fatalf("isolated call = %q %q %q %q %q", controllerURL, logsURL, runID, nodeID, token)
		}
		return runner.Result{Outcome: sparkwing.Success}, nil
	}
	node := &store.Node{
		RunID: "run-isolated", NodeID: "build", ClaimedBy: "holder", ClaimGeneration: 1,
		ClaimMembershipID: "membership", ReservationID: "reservation",
	}
	result, err := RunNodeOnce(ctx, server.URL, "", node.RunID, node.NodeID, node.ClaimedBy, "parent-token",
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, ClaimedNodeAttempt(node))
	if err != nil {
		t.Fatal(err)
	}
	if !called || result.Outcome != sparkwing.Success {
		t.Fatalf("claimed registered node bypassed isolated child: called=%v result=%+v", called, result)
	}
}
