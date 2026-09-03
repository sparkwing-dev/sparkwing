package orchestrator

import (
	"context"
	"errors"
	"net"
	"os"
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
