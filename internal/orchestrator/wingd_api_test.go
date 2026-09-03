package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestWingdAPIServesARunOverTheSocketAndMintsNoToken(t *testing.T) {
	home := wingdTestHome(t)
	sock, runs := startAPIDaemon(t, home, nil)
	if _, err := os.Stat(PathsAt(home).StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat the runs store before the first request: %v, want it absent", err)
	}

	httpClient := apiHTTPClient(sock)
	c := client.New(apiBaseURL, httpClient)
	ctx := context.Background()
	seedRun(t, c, "r1", "n1")

	run, err := c.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Pipeline != "p" {
		t.Fatalf("GetRun returned pipeline %q, want %q", run.Pipeline, "p")
	}
	list, err := c.ListRuns(ctx, store.RunFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(list) != 1 || list[0].ID != "r1" {
		t.Fatalf("ListRuns returned %d runs, want the one the client created", len(list))
	}
	if err := c.TouchNodeHeartbeat(ctx, "r1", "n1"); err != nil {
		t.Fatalf("TouchNodeHeartbeat: %v", err)
	}
	if err := c.AppendEvent(ctx, "r1", "n1", "node.log", []byte(`{"line":"hello"}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := c.FinishNode(ctx, "r1", "n1", "success", "", nil); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	nodes, err := c.ListNodes(ctx, "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Outcome != "success" {
		t.Fatalf("ListNodes returned %d nodes, want one that succeeded", len(nodes))
	}
	if err := c.FinishRun(ctx, "r1", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	conc := NewHTTPConcurrency(apiBaseURL, httpClient, "", 30*time.Second)
	acquired, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: "memo:m1", RunID: "r1", NodeID: "n1", HolderID: "h1",
		Policy: "memoize", Lease: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if acquired.Kind != store.AcquireGranted {
		t.Fatalf("AcquireSlot returned %q, want %q", acquired.Kind, store.AcquireGranted)
	}
	if _, _, err := conc.HeartbeatSlot(ctx, "memo:m1", "h1", 30*time.Second); err != nil {
		t.Fatalf("HeartbeatSlot: %v", err)
	}
	if err := conc.ReleaseSlot(ctx, "memo:m1", "h1", "success", "", "", time.Minute); err != nil {
		t.Fatalf("ReleaseSlot: %v", err)
	}

	st, err := runs.Store(ctx, false)
	if err != nil {
		t.Fatalf("the daemon's own handle: %v", err)
	}
	held, err := st.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("the daemon's held store does not carry the run: %v", err)
	}
	if held.Status != "success" {
		t.Fatalf("the held store reports status %q, want %q", held.Status, "success")
	}
	tokens, err := st.ListTokens("", true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("the API minted %d token(s); a peer-uid caller must need none", len(tokens))
	}
}

func TestWingdAPIListenerMovesToTheSuccessor(t *testing.T) {
	home := wingdTestHome(t)
	createStore(t, home)
	sock, _ := startAPIDaemon(t, home, nil)

	first := client.New(apiBaseURL, apiHTTPClient(sock))
	ctx := context.Background()
	seedRun(t, first, "r1", "n1")

	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := wingdclient.Stop(stopCtx, wingdclient.Options{Home: home, Version: "test"}); err != nil {
		t.Fatalf("drain the predecessor: %v", err)
	}
	if nc, err := net.Dial("unix", sock); err == nil {
		_ = nc.Close()
		t.Fatal("the drained daemon still serves the API socket")
	}

	successorSock, _ := startAPIDaemon(t, home, nil)
	if successorSock != sock {
		t.Fatalf("the successor bound %s, want the same %s", successorSock, sock)
	}
	second := client.New(apiBaseURL, apiHTTPClient(sock))
	run, err := second.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("the successor does not serve the API: %v", err)
	}
	if run.ID != "r1" {
		t.Fatalf("the successor returned run %q, want r1", run.ID)
	}
}

func TestWingdAPIStatusReportsTheSocket(t *testing.T) {
	home := wingdTestHome(t)
	createStore(t, home)
	sock, _ := startAPIDaemon(t, home, nil)

	path, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatalf("APISocketPath: %v", err)
	}
	if path != sock {
		t.Fatalf("APISocketPath = %s, want %s", path, sock)
	}
	dSock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := wingdclient.Probe(ctx, dSock)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.APIReady == nil || !*info.APIReady {
		t.Fatalf("the probe reports api_ready %v, want true", info.APIReady)
	}
}

func TestRevokingATokenTakesEffectOnTheReadRoutes(t *testing.T) {
	home := wingdTestHome(t)
	createStore(t, home)
	sock, _ := startAPIDaemon(t, home, nil)
	httpClient := apiHTTPClient(sock)

	raw, prefix := mintAPIToken(t, httpClient)
	readRoute := apiBaseURL + "/api/v1/triggers"
	writeRoute := apiBaseURL + "/api/v1/trends"
	if got := statusWithToken(t, httpClient, readRoute, raw); got != http.StatusOK {
		t.Fatalf("the read route answered %d before the revoke, want 200", got)
	}
	if got := statusWithToken(t, httpClient, writeRoute, raw); got != http.StatusOK {
		t.Fatalf("the write route answered %d before the revoke, want 200", got)
	}

	req, err := http.NewRequest(http.MethodDelete, apiBaseURL+"/api/v1/tokens/"+prefix, nil)
	if err != nil {
		t.Fatalf("build the revoke: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke answered %d, want 204", resp.StatusCode)
	}

	if got := statusWithToken(t, httpClient, readRoute, raw); got != http.StatusUnauthorized {
		t.Fatalf("the read route answered %d after the revoke, want 401; the two servers hold separate token caches", got)
	}
	if got := statusWithToken(t, httpClient, writeRoute, raw); got != http.StatusUnauthorized {
		t.Fatalf("the write route answered %d after the revoke, want 401", got)
	}
}

func mintAPIToken(t *testing.T, httpClient *http.Client) (raw, prefix string) {
	t.Helper()
	body := strings.NewReader(`{"principal":"probe","kind":"service","scopes":["admin"]}`)
	resp, err := httpClient.Post(apiBaseURL+"/api/v1/tokens", "application/json", body)
	if err != nil {
		t.Fatalf("mint a token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("mint a token answered %d", resp.StatusCode)
	}
	var minted struct {
		Token    string `json:"token"`
		Metadata struct {
			Prefix string `json:"prefix"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&minted); err != nil {
		t.Fatalf("decode the minted token: %v", err)
	}
	if minted.Token == "" || minted.Metadata.Prefix == "" {
		t.Fatalf("the mint answered no token or prefix: %+v", minted)
	}
	return minted.Token, minted.Metadata.Prefix
}

func statusWithToken(t *testing.T, httpClient *http.Client, url, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestAStreamingRouteOutlivesTheRequestBound(t *testing.T) {
	home := wingdTestHome(t)
	createStore(t, home)
	const bound = 300 * time.Millisecond
	sock, _ := startAPIDaemonWith(t, home, nil, func(a *wingdAPI) { a.requestTimeout = bound })
	httpClient := apiHTTPClient(sock)
	c := client.New(apiBaseURL, httpClient)
	seedRun(t, c, "r1", "n1")
	seedRun(t, c, "r2", "n2")

	conc := NewHTTPConcurrency(apiBaseURL, httpClient, "", 30*time.Second)
	ctx := context.Background()
	if _, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: "memo:m1", RunID: "r1", NodeID: "n1", HolderID: "h1",
		Policy: "memoize", Lease: 30 * time.Second,
	}); err != nil {
		t.Fatalf("AcquireSlot for the holder: %v", err)
	}
	if _, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: "memo:m1", RunID: "r2", NodeID: "n2", HolderID: "h2",
		Policy: "memoize", Lease: 30 * time.Second,
	}); err != nil {
		t.Fatalf("AcquireSlot for the waiter: %v", err)
	}

	stream := &http.Client{Transport: httpClient.Transport}
	resp, err := stream.Get(apiBaseURL + "/api/v1/concurrency/" + url.PathEscape("memo:m1") + "/notify?run_id=r2&node_id=n2")
	if err != nil {
		t.Fatalf("open the notify stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the notify stream answered %d, want 200", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read the stream preamble: %v", err)
	}

	// safety: the waiter stays waiting, so a live stream sends nothing and a
	// truncated one closes; only the closed case is a failure.
	closed := make(chan error, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				closed <- err
				return
			}
			t.Logf("stream: %q", line)
		}
	}()
	select {
	case err := <-closed:
		t.Fatalf("the notify stream ended after opening, inside the %s request bound: %v", bound, err)
	case <-time.After(5 * bound):
	}
}

func TestAReadRouteDoesNotCreateTheRunsStore(t *testing.T) {
	home := wingdTestHome(t)
	sock, _ := startAPIDaemon(t, home, nil)
	httpClient := apiHTTPClient(sock)

	resp, err := httpClient.Get(apiBaseURL + "/api/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("health answered %d on a home with no store, want 503: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Retry-After") != "" {
		t.Errorf("an absent store invited the caller back with Retry-After %q", resp.Header.Get("Retry-After"))
	}
	if _, err := os.Stat(PathsAt(home).StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unauthenticated health probe created the runs store")
	}

	c := client.New(apiBaseURL, httpClient)
	if err := c.CreateRun(context.Background(), store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := os.Stat(PathsAt(home).StateDB()); err != nil {
		t.Fatalf("a state request did not create the runs store: %v", err)
	}
}

func TestDaemonServesWhenTheCacheURLWillNotResolve(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "bogus-scheme://nowhere")
	art, fault := wingdArtifactStore(context.Background())
	if art != nil {
		t.Fatalf("a cache URL that will not open resolved to %T", art)
	}
	if fault == "" {
		t.Fatal("a cache URL that will not open reported no fault")
	}

	home := wingdTestHome(t)
	createStore(t, home)
	startAPIDaemon(t, home, func(cfg *wingd.Config) { cfg.ArtifactStoreError = fault })

	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := wingdclient.Probe(ctx, sock)
	if err != nil {
		t.Fatalf("the daemon did not serve with an unresolvable cache URL: %v", err)
	}
	if info.ArtifactStoreError != fault {
		t.Fatalf("the daemon reports artifact store error %q, want %q", info.ArtifactStoreError, fault)
	}
	if info.APIReady == nil || !*info.APIReady {
		t.Fatalf("the daemon reports api_ready %v, want true", info.APIReady)
	}
}
