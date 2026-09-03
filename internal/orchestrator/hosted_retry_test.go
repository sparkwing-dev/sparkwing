package orchestrator

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestHostedRetryPolicy_ClassifiesEveryWriteTheRunMakes(t *testing.T) {
	cases := []struct {
		method, path string
		want         hostedRetryPolicy
	}{
		{"GET", "/api/v1/runs/r1", hostedRetryRepeatable},
		{"POST", "/api/v1/runs", hostedRetryCreate},
		{"POST", "/api/v1/runs/r1/nodes", hostedRetryCreate},
		{"POST", "/api/v1/runs/r1/finish", hostedRetryRepeatable},
		{"POST", "/api/v1/runs/r1/heartbeat", hostedRetryRepeatable},
		{"POST", "/api/v1/runs/r1/nodes/n1/start", hostedRetryRepeatable},
		{"POST", "/api/v1/runs/r1/nodes/n1/finish", hostedRetryRepeatable},
		{"POST", "/api/v1/runs/r1/nodes/n1/status", hostedRetryRepeatable},
		{"POST", "/api/v1/runs/r1/nodes/n1/deps", hostedRetryRepeatable},
		{"POST", "/api/v1/runs/r1/nodes/n1/steps/skip", hostedRetryRepeatable},
		{"POST", "/api/v1/concurrency/k/acquire", hostedRetryRepeatable},
		{"POST", "/api/v1/concurrency/k/release", hostedRetryRepeatable},
		{"POST", "/api/v1/runs/r1/events", hostedRetryUnsent},
		{"POST", "/api/v1/runs/r1/nodes/n1/dispatch", hostedRetryUnsent},
		{"POST", "/api/v1/runs/r1/nodes/n1/usage", hostedRetryUnsent},
		{"POST", "/api/v1/runs/r1/nodes/n1/metrics", hostedRetryUnsent},
		{"POST", "/api/v1/runs/r1/nodes/n1/annotations", hostedRetryUnsent},
		{"POST", "/api/v1/pipelines/p/profile/observations", hostedRetryUnsent},
		{"POST", "/api/v1/triggers", hostedRetryUnsent},
		{"POST", "/api/v1/runs/r1/retry", hostedRetryUnsent},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, "http://sparkwing-api"+tc.path, nil)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		if got := hostedRetryPolicyFor(req); got != tc.want {
			t.Errorf("%s %s policy = %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
}

// restartableAPI serves a handler on a unix socket path a test can take away
// and give back, which is what a daemon restart looks like to a hosted run.
type restartableAPI struct {
	t    *testing.T
	sock string
	h    http.Handler

	mu  sync.Mutex
	srv *http.Server
}

func newRestartableAPI(t *testing.T, h http.Handler) *restartableAPI {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swrestart")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	a := &restartableAPI{t: t, sock: filepath.Join(dir, "api.sock"), h: h}
	a.start()
	t.Cleanup(a.stop)
	return a
}

func (a *restartableAPI) start() {
	a.t.Helper()
	ln, err := net.Listen("unix", a.sock)
	if err != nil {
		a.t.Fatalf("listen %s: %v", a.sock, err)
	}
	srv := &http.Server{Handler: a.h, ReadHeaderTimeout: 5 * time.Second}
	a.mu.Lock()
	a.srv = srv
	a.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
}

func (a *restartableAPI) stop() {
	a.mu.Lock()
	srv := a.srv
	a.srv = nil
	a.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	_ = os.Remove(a.sock)
}

func TestHostedRetry_SurvivesADaemonRestartBetweenWrites(t *testing.T) {
	home := wingdTestHome(t)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	api := newRestartableAPI(t, controller.New(st, nil).
		WithPeerPrincipal(testAdminPrincipal).
		Handler())
	backends, release := HostedBackends(paths, api.sock, nil)
	t.Cleanup(release)

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	if err := backends.State.CreateRun(ctx, store.Run{
		ID: "restart-run", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun before the restart: %v", err)
	}

	api.stop()
	restarted := make(chan struct{})
	go func() {
		defer close(restarted)
		time.Sleep(400 * time.Millisecond)
		api.start()
	}()

	if err := backends.State.CreateNode(ctx, store.Node{
		RunID: "restart-run", NodeID: "n1", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode across the restart: %v", err)
	}
	<-restarted

	nodes, err := st.ListNodes(ctx, "restart-run")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want the write to have landed exactly once", len(nodes))
	}
}

func testAdminPrincipal(*http.Request) *controller.Principal {
	return &controller.Principal{
		Name:   "unix-peer:test",
		Kind:   "service",
		Scopes: []string{controller.ScopeAdmin},
		Authed: time.Now().UTC(),
	}
}

func TestHostedRetry_ConflictOnARetryIsTheFirstAttemptLanding(t *testing.T) {
	var attempts atomic.Int32
	api := newRestartableAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			// safety: the row landed and the answer never arrived, which is
			// the only state in which a retry can meet its own write.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("no hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"UNIQUE constraint failed: nodes.run_id, nodes.node_id"}`))
	}))

	c := client.New(HostedAPIBaseURL, NewAPISocketClient(api.sock))
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	if err := c.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode = %v, want the conflict read as the first attempt landing", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestHostedRetry_DoesNotRepeatAnAppendTheDaemonMayHaveTaken(t *testing.T) {
	var attempts atomic.Int32
	api := newRestartableAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))

	c := client.New(HostedAPIBaseURL, NewAPISocketClient(api.sock))
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	if err := c.AppendEvent(ctx, "r1", "n1", "kind", []byte(`{}`)); err == nil {
		t.Fatal("AppendEvent succeeded against a daemon that answered nothing")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("the event reached the daemon %d times, want 1", got)
	}
}

func TestHostedRetry_GivesUpNamingTheDaemon(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "api.sock")
	transport := newHostedRetryTransport(apiSocketTransport(sock))
	transport.budget = 300 * time.Millisecond
	c := client.New(HostedAPIBaseURL, &http.Client{Timeout: wingdTestWait, Transport: transport})

	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	started := time.Now()
	err := c.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"})
	if err == nil {
		t.Fatal("CreateNode succeeded with no daemon listening")
	}
	if elapsed := time.Since(started); elapsed > wingdTestWait/2 {
		t.Fatalf("gave up after %s, want it bounded by the budget", elapsed)
	}
	if !strings.Contains(err.Error(), "api.sock") {
		t.Fatalf("err = %v, want it to name the socket it could not reach", err)
	}
}

func TestNodeTransports_LogsLeaveTheAPISocketAlone(t *testing.T) {
	var socketHits, tcpHits atomic.Int32
	sock := serveStubAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		socketHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	logs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tcpHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer logs.Close()

	transports := nodeTransportsFor(runNodeConfig{apiSocket: sock}, "http://127.0.0.1:1", "svc_tok")
	defer transports.close()
	if transports.stateURL != HostedAPIBaseURL || transports.stateToken != "" {
		t.Fatalf("state target = %q token %q, want the socket and no bearer",
			transports.stateURL, transports.stateToken)
	}

	backend := NewHTTPLogsWithToken(logs.URL, transports.plain, "svc_tok", nil)
	nodeLog, err := backend.OpenNodeLog("r1", "n1", nil)
	if err != nil {
		t.Fatalf("OpenNodeLog: %v", err)
	}
	nodeLog.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Msg: "hello"})
	_ = nodeLog.Close()

	if tcpHits.Load() == 0 {
		t.Fatal("no log record reached the logs service")
	}
	if got := socketHits.Load(); got != 0 {
		t.Fatalf("%d log record(s) went down the API socket", got)
	}
}
