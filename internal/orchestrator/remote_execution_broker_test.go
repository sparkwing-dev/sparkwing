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
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRemoteExecutionChildEnvironmentDropsSupervisorAuthority(t *testing.T) {
	private := []string{
		"SPARKWING_AGENT_TOKEN=parent-token",
		"SPARKWING_CACHE_TOKEN=operator-cache-token",
		"SPARKWING_CACHE_GRANT=swcg1.stale-grant",
		"SPARKWING_RUN_HANDLE_FILE=/tmp/parent-run.json",
		"SPARKWING_ONLY=outer",
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

func TestRemoteExecutionBrokerUploadsOwnNodeOutput(t *testing.T) {
	data := []byte(`{"value":"from-producer"}`)
	sum := sha256.Sum256(data)
	uploaded := make(chan []byte, 1)
	blobs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get(store.ClaimHolderHeader) != "" {
			t.Error("supervisor credentials reached the external output store")
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
			return
		}
		if r.Method != http.MethodPut {
			t.Errorf("blob request method = %s", r.Method)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		uploaded <- data
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(blobs.Close)
	committed := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer parent-token" || r.Header.Get(store.ClaimHolderHeader) != "holder" {
			t.Error("output request lacks supervisor claim authority")
		}
		switch r.URL.Path {
		case "/api/v1/runs/run-1/nodes/producer/output":
			if _, forwarded := r.Header["X-Forwarded-For"]; forwarded || r.Header.Get("X-Forwarded-Host") != "" {
				http.Error(w, "CloudFront signing unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(store.OutputReadGrant{URL: blobs.URL, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), SourceRunID: "run-1"})
		case "/api/v1/runs/run-1/nodes/node-a/output-upload":
			_ = json.NewEncoder(w).Encode(store.OutputUploadGrant{UploadID: "upload-1", Key: "outputs/node-a", URL: blobs.URL})
		case "/api/v1/runs/run-1/nodes/node-a/output-commit":
			committed <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected output route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	broker, err := startRemoteExecutionBroker(upstream.URL, "", "parent-token", "run-1", "node-a", store.NodeClaimFence{HolderID: "holder"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	c := client.NewWithToken(broker.URL(), nil, broker.capability)
	ref, err := c.UploadNodeOutput(context.Background(), "run-1", "node-a", data)
	if err != nil {
		t.Fatalf("upload own output: %v", err)
	}
	got := <-uploaded
	if string(got) != string(data) || ref.Key != "outputs/node-a" {
		t.Fatalf("output = %q, ref = %+v", got, ref)
	}
	select {
	case <-committed:
	default:
		t.Fatal("output upload was not committed")
	}
	output, err := c.GetNodeOutput(context.Background(), "run-1", "producer")
	if err != nil || string(output) != string(data) {
		t.Fatalf("read producer output = %q, %v", output, err)
	}
	for _, target := range []struct{ run, node string }{{"run-2", "node-a"}, {"run-1", "node-b"}} {
		if _, err := c.UploadNodeOutput(context.Background(), target.run, target.node, data); err == nil {
			t.Fatalf("uploaded another claim's output: %+v", target)
		}
	}
}

func TestRemoteExecutionBrokerFilesystemOutputs(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint("external=", external), func(t *testing.T) {
			st, err := teststore.Open(t.TempDir() + "/state.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			st.SetOutputDir(t.TempDir())
			ctx := context.Background()
			if err := st.CreateRun(ctx, store.Run{ID: "run-fs", Pipeline: "fs", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "run-fs", NodeID: "produce", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			token, _, err := st.CreateToken("operator", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			srv := controller.New(st, nil).EnableAuthFromStore()
			blobRequests := make(chan string, 4)
			handler := func(name string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/api/v1/outputs/") {
						if r.Header.Get("Authorization") != "" || r.Header.Get(store.ClaimHolderHeader) != "" {
							t.Error("claim authority reached a signed filesystem URL")
						}
						blobRequests <- name + " " + r.Method
					}
					srv.Handler().ServeHTTP(w, r)
				})
			}
			upstream := httptest.NewServer(handler("controller"))
			t.Cleanup(upstream.Close)
			if external {
				alias := httptest.NewServer(handler("external"))
				t.Cleanup(alias.Close)
				srv.WithExternalURL(alias.URL)
			}
			producer, err := startRemoteExecutionBroker(upstream.URL, "", token, "run-fs", "produce", store.NodeClaimFence{}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(producer.Close)
			data := []byte(`{"value":"filesystem-producer"}`)
			writer := client.NewWithToken(producer.URL(), nil, producer.capability)
			if err := writer.FinishNode(ctx, "run-fs", "produce", "success", "", data); err != nil {
				t.Fatalf("producer output: %v", err)
			}
			consumer, err := startRemoteExecutionBroker(upstream.URL, "", token, "run-fs", "consume", store.NodeClaimFence{}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(consumer.Close)
			reader := client.NewWithToken(consumer.URL(), nil, consumer.capability)
			output, err := reader.GetNodeOutput(ctx, "run-fs", "produce")
			if err != nil || string(output) != string(data) {
				t.Fatalf("consumer output = %q, %v", output, err)
			}
			wantHost := "controller"
			if external {
				wantHost = "external"
			}
			for _, method := range []string{http.MethodPut, http.MethodGet} {
				if got := <-blobRequests; got != wantHost+" "+method {
					t.Fatalf("signed byte request = %q, want %s %s", got, wantHost, method)
				}
			}
			if _, err := reader.UploadNodeOutput(ctx, "run-fs", "produce", data); err == nil {
				t.Fatal("consumer wrote the producer's output")
			}
		})
	}
}

func TestRemoteExecutionBrokerMemoizedSlot(t *testing.T) {
	var forwarded []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = append(forwarded, r.Method+" "+r.URL.EscapedPath())
		switch {
		case strings.HasSuffix(r.URL.Path, "/acquire"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"kind":"granted","granted":true,"holder_id":"run-1/node-a"}`)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/release"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/cancel-waiter"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"cancelled":true}`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer upstream.Close()
	broker, err := startRemoteExecutionBroker(upstream.URL, "", "team-token", "run-1", "node-a", store.NodeClaimFence{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	key := "memo:ck:hash"
	child := NewHTTPConcurrency(broker.URL(), nil, broker.capability, time.Minute)
	got, err := child.AcquireSlot(context.Background(), store.AcquireSlotRequest{Key: key, RunID: "run-1", NodeID: "node-a", HolderID: "run-1/node-a", CacheKeyHash: "hash"})
	if err != nil || got.Kind != store.AcquireGranted {
		t.Fatalf("memo acquire = %+v, %v", got, err)
	}
	if _, _, err := child.HeartbeatSlot(context.Background(), key, "run-1/node-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := child.ReleaseSlot(context.Background(), key, "run-1/node-a", "success", "run-1/node-a", "hash", time.Hour); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := child.CancelWaiter(context.Background(), key, "run-1", "node-a"); err != nil || !cancelled {
		t.Fatalf("cancel own waiter = %v, %v", cancelled, err)
	}
	if len(forwarded) != 4 {
		t.Fatalf("forwarded routes = %v", forwarded)
	}

	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/concurrency/" + key + "/acquire", `{"run_id":"run-2","node_id":"node-a","holder_id":"run-2/node-a"}`},
		{http.MethodPost, "/api/v1/concurrency/" + key + "/acquire", `{"run_id":"run-1","node_id":"node-b","holder_id":"run-1/node-b"}`},
		{http.MethodPost, "/api/v1/concurrency/" + key + "/release", `{"holder_id":"run-2/node-a","outcome":"success"}`},
		{http.MethodPost, "/api/v1/concurrency/" + key + "/heartbeat", `{"holder_id":"run-2/node-a"}`},
		{http.MethodPost, "/api/v1/concurrency/" + key + "/cancel-waiter", `{"run_id":"run-2","node_id":"node-a"}`},
		{http.MethodPost, "/api/v1/concurrency/" + key + "/force-release", `{}`},
	} {
		r, err := http.NewRequest(req.method, broker.URL()+req.path, strings.NewReader(req.body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+broker.capability)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", req.method, req.path, resp.StatusCode)
		}
	}
	if len(forwarded) != 4 {
		t.Fatalf("foreign slot request reached controller: %v", forwarded)
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
	st, err := teststore.Open(t.TempDir() + "/state.db")
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
	runNodeIsolatedFn = func(_ context.Context, controllerURL, logsURL, runID, nodeID, token, _ string, _ *slog.Logger) (runner.Result, error) {
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

func TestRemoteExecutionBrokerConsumerReadsProducerMetadata(t *testing.T) {
	st, err := teststore.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "artifact-demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"produce", "consume"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "passed"}); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "sha256:" + strings.Repeat("a", 64)
	if err := st.SetNodeArtifactManifest(ctx, "run-1", "produce", manifest); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer upstream.Close()
	broker, err := startRemoteExecutionBroker(upstream.URL, "", "parent-token", "run-1", "consume", store.NodeClaimFence{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	state := client.NewWithToken(broker.URL(), http.DefaultClient, broker.capability)
	for _, id := range []string{"consume", "produce"} {
		node, err := state.GetNode(ctx, "run-1", id)
		if err != nil {
			t.Fatalf("GetNode(%q): %v", id, err)
		}
		if node.NodeID != id {
			t.Fatalf("node = %q, want %q", node.NodeID, id)
		}
		if id == "produce" && node.ArtifactManifest != manifest {
			t.Fatalf("manifest = %q, want %q", node.ArtifactManifest, manifest)
		}
	}
}

func TestRemoteExecutionBrokerProducerMetadataScope(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer upstream.Close()
	broker, err := startRemoteExecutionBroker(upstream.URL, "", "parent-token", "run-1", "consume", store.NodeClaimFence{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/runs/run-2/nodes/produce"},
		{http.MethodGet, "/api/v1/runs/run-10/nodes/produce"},
		{http.MethodPost, "/api/v1/runs/run-1/nodes/produce"},
		{http.MethodPost, "/api/v1/runs/run-1/nodes/produce/artifact-manifest"},
		{http.MethodGet, "/api/v1/runs/run-1/nodes/produce/metrics"},
		{http.MethodGet, "/api/v1/runs/run-1/nodes/%2e%2e"},
		{http.MethodGet, "/api/v1/runs/run-1/nodes/produce%2fmetrics"},
		{http.MethodGet, "/api/v1/runs/run-1/nodes/"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), tc.method, broker.URL()+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+broker.capability)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}
