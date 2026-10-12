package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type brokeredRunnerNode struct {
	store  *store.Store
	url    string
	token  string
	fence  store.NodeClaimFence
	broker *remoteExecutionBroker
	child  *client.Client
}

func newBrokeredRunnerNode(t *testing.T, configure func(*controller.Server, string) *controller.Server) brokeredRunnerNode {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	token, _, err := st.CreateToken("runner-a", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeRunsState,
		controller.ScopeSecretsRead, controller.ScopeLogsWrite,
	}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "parent", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "parent"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(nil)
	c := controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).EnableAuthFromStore()
	if configure != nil {
		c = configure(c, "http://"+srv.Listener.Addr().String())
	}
	srv.Config.Handler = c.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	n, err := client.NewWithToken(srv.URL, nil, token).ClaimNode(ctx, "runner:box-a:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.NodeID != "parent" {
		t.Fatalf("claim parent = %+v, %v", n, err)
	}
	fence := store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	broker, err := startRemoteExecutionBroker(srv.URL, "", token, "run-1", "parent", fence, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	return brokeredRunnerNode{
		store: st, url: srv.URL, token: token, fence: fence, broker: broker,
		child: client.NewWithToken(broker.URL(), nil, broker.capability),
	}
}

const brokerRefusal = "execution capability does not allow this route"

func TestBrokeredNodeMintsAnOIDCToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signing := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	f := newBrokeredRunnerNode(t, func(c *controller.Server, issuer string) *controller.Server {
		iss, err := oidcissuer.New(issuer, signing, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return c.WithOIDCIssuer(iss)
	})
	ctx := context.Background()
	direct, err := client.NewWithToken(f.url, nil, f.token).OIDCToken(store.WithNodeClaimFence(ctx, f.fence), "run-1", "sts.amazonaws.com")
	if err != nil || direct.Token == "" {
		t.Fatalf("direct mint = %+v, %v", direct, err)
	}
	brokered, err := f.child.OIDCToken(ctx, "run-1", "sts.amazonaws.com")
	if err != nil || brokered.Token == "" {
		t.Fatalf("brokered mint = %+v, %v", brokered, err)
	}
	if _, err := f.child.OIDCToken(ctx, "run-2", "sts.amazonaws.com"); err == nil || !strings.Contains(err.Error(), brokerRefusal) {
		t.Fatalf("mint for another run = %v, want the broker's refusal", err)
	}
}

// hack: the upstream credential is admin, as the local loopback controller's run token is, so the
// controller authorizes what the broker forwards and the test sees the feature itself work.
func adminBrokeredNode(t *testing.T, nodeID string, wrap ...func(http.Handler) http.Handler) (*store.Store, *remoteExecutionBroker, *client.Client) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, _, err := st.CreateToken("local-run", store.TokenKindService, []string{controller.ScopeAdmin}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: nodeID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	handler := controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).EnableAuthFromStore().Handler()
	for _, w := range wrap {
		handler = w(handler)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	broker, err := startRemoteExecutionBroker(srv.URL, "", token, "run-1", nodeID, store.NodeClaimFence{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	return st, broker, client.NewWithToken(broker.URL(), nil, broker.capability)
}

func TestBrokeredNodeCreatesNoNodes(t *testing.T) {
	_, _, child := adminBrokeredNode(t, "parent")
	ctx := context.Background()
	for _, id := range []string{"parent/scan", "sibling"} {
		if err := child.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "pending"}); err == nil || !strings.Contains(err.Error(), brokerRefusal) {
			t.Errorf("create %q = %v, want the broker's refusal", id, err)
		}
	}
	if err := child.StartNode(ctx, "run-1", "parent/scan"); err == nil || !strings.Contains(err.Error(), brokerRefusal) {
		t.Errorf("start a node under this one = %v, want the broker's refusal", err)
	}
}

func TestBrokeredNodeAwaitsAChildRun(t *testing.T) {
	triggered := make(chan struct{}, 1)
	st, broker, child := adminBrokeredNode(t, "parent", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers" {
				triggered <- struct{}{}
			}
		})
	})
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "run-1", Pipeline: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		<-triggered
		finished <- func() error {
			triggers, err := st.ListTriggers(ctx, store.TriggerFilter{})
			if err != nil {
				return err
			}
			for _, tr := range triggers {
				if tr.ParentRunID != "run-1" {
					continue
				}
				if _, err := st.GetRun(ctx, tr.ID); errors.Is(err, store.ErrNotFound) {
					if err := st.CreateRun(ctx, store.Run{ID: tr.ID, Pipeline: "child", Status: "running", StartedAt: time.Now()}); err != nil {
						return err
					}
				}
				if err := st.CreateNode(ctx, store.Node{RunID: tr.ID, NodeID: "report", Status: "running"}); err != nil {
					return err
				}
				if err := st.FinishNode(ctx, tr.ID, "report", "success", "", []byte(`{"ok":true}`)); err != nil {
					return err
				}
				return st.FinishRun(ctx, tr.ID, "success", "")
			}
			return errors.New("no child trigger names run-1 as its parent")
		}()
	}()
	await := childAwaitConfig{
		state:       child,
		concurrency: NewHTTPConcurrency(broker.URL(), nil, broker.capability, time.Minute),
		parentRunID: "run-1",
		retryOf:     "run-0",
		masker:      secrets.NewMasker(),
		diagnostics: podChildAwaitDiagnostics{logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		pollFactory: func() (childAwaitPollPolicy, error) { return &retryChildAwaitPoll{}, nil },
	}
	resolved, err := await.await(sparkwingruntime.WithNode(ctx, "parent"),
		sparkwing.AwaitRequest{Pipeline: "child", NodeID: "report", Timeout: time.Minute})
	if err != nil {
		t.Fatalf("RunAndAwait through the broker: %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if string(resolved.Data) != `{"ok":true}` {
		t.Fatalf("child output = %s", resolved.Data)
	}
	if _, err := await.concurrency.State(ctx, "group"); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("child admission state read: %v", err)
	}

	for _, req := range []client.TriggerRequest{
		{Pipeline: "child"},
		{Pipeline: "child", ParentRunID: "run-2", ParentNodeID: "parent"},
		{Pipeline: "child", ParentRunID: "run-1", ParentNodeID: "sibling"},
		{Pipeline: "child", ParentRunID: "run-1", ParentNodeID: "parent/child"},
	} {
		if _, err := child.CreateTrigger(ctx, req); err == nil || !strings.Contains(err.Error(), brokerRefusal) {
			t.Errorf("trigger %+v = %v, want the broker's refusal", req, err)
		}
	}
	if _, err := child.FindSpawnedChildTriggerID(ctx, "run-0", "sibling", "child"); err == nil || !strings.Contains(err.Error(), brokerRefusal) {
		t.Errorf("a sibling's spawned child = %v, want the broker's refusal", err)
	}
	if _, err := child.GetRunForExecution(ctx, resolved.RunID); err == nil || !strings.Contains(err.Error(), brokerRefusal) {
		t.Errorf("another run's secret arguments = %v, want the broker's refusal", err)
	}
	if _, err := child.GetRunForExecution(ctx, "run-1"); err != nil {
		t.Errorf("own run's execution read: %v", err)
	}
}

func TestBrokeredNodeReadsAnotherRunsOutputs(t *testing.T) {
	st, _, child := adminBrokeredNode(t, "consume")
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-0", Pipeline: "upstream", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-0", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "run-0", "build", "success", "", []byte(`{"image":"app:1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "run-0", "success", ""); err != nil {
		t.Fatal(err)
	}

	resolve := newPipelineRefResolver(child, "run-1", func(_ context.Context, _ string, err error) {
		t.Errorf("pipeline ref audit event: %v", err)
	})
	ref, err := resolve(sparkwingruntime.WithNode(ctx, "consume"), "upstream", "build", time.Hour)
	if err != nil {
		t.Fatalf("pipeline ref through the broker: %v", err)
	}
	if ref.RunID != "run-0" || string(ref.Data) != `{"image":"app:1"}` {
		t.Fatalf("pipeline ref = %+v", ref)
	}

	executor := NewNodeExecutor(RemoteBackends(child, nil, nil, nil, time.Minute))
	cached, err := executor.fetchCachedOutput(ctx, coordinationParameters{}, "run-0", "build")
	if err != nil || string(cached) != `{"image":"app:1"}` {
		t.Fatalf("cache-hit output from the origin run = %s, %v", cached, err)
	}
}

func TestBrokeredNodeForceReleasesSupersededHolders(t *testing.T) {
	st, broker, _ := adminBrokeredNode(t, "newer")
	ctx := context.Background()
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "older", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "deploy", HolderID: "run-1/older", RunID: "run-1", NodeID: "older", Capacity: 1, Lease: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	conc := NewHTTPConcurrency(broker.URL(), nil, broker.capability, time.Minute)
	got, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: "deploy", HolderID: "run-1/newer", RunID: "run-1", NodeID: "newer", Capacity: 1,
		Policy: store.OnLimitCancelOthers, Lease: time.Minute,
	})
	if err != nil || got.Kind != store.AcquireCancellingOthers {
		t.Fatalf("acquire with cancel-others = %+v, %v", got, err)
	}
	dropped, err := conc.ForceReleaseSuperseded(ctx, "deploy")
	if err != nil {
		t.Fatalf("force-release through the broker: %v", err)
	}
	if len(dropped) != 1 || dropped[0].HolderID != "run-1/older" {
		t.Fatalf("dropped = %+v, want the superseded holder", dropped)
	}
	if _, err := conc.ForceReleaseSuperseded(ctx, "a/b"); err == nil {
		t.Fatal("force-release of a multi-segment key was forwarded")
	}
}

func TestBrokeredNodeMovesOutputsThroughTheLoopbackShim(t *testing.T) {
	home := t.TempDir()
	paths := PathsAt(home)
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"produce", "consume"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	backends := LocalBackends(paths, st, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	shim, err := startLoopbackShim(backends.State, backends.Concurrency, "run-1", logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shim.Close)
	brokered := func(node string) *client.Client {
		b, err := startRemoteExecutionBroker(shim.url, "", shim.token, "run-1", node, store.NodeClaimFence{}, nil, logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(b.Close)
		return client.NewWithToken(b.URL(), nil, b.capability)
	}
	data := []byte(`{"digest":"sha256:abc"}`)
	if err := brokered("produce").FinishNode(ctx, "run-1", "produce", "success", "", data); err != nil {
		t.Fatalf("finish with an output through the broker: %v", err)
	}
	got, err := brokered("consume").GetNodeOutput(ctx, "run-1", "produce")
	if err != nil || string(got) != string(data) {
		t.Fatalf("read the output through the broker = %s, %v", got, err)
	}
}
