package orchestrator

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const apiBaseURL = "http://sparkwing"

func apiHTTPClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
}

func startAPIDaemon(t *testing.T, home string, tune func(*wingd.Config)) (string, *HeldRunStore) {
	t.Helper()
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	api := newWingdAPI(runs, nil, nil)
	cfg := wingd.Config{
		Home:    home,
		Version: "test",
		Sampler: stubSampler{wingd.HostStat{
			TotalCores: 8, TotalMemoryBytes: 64 << 30, FreeMemoryBytes: 64 << 30,
			LoadMeasured: true, MemoryMeasured: true,
		}},
		HeadroomFraction: -1,
		GraceWindow:      -1,
		Runs:             runs,
		ServeAPI:         api.serve,
	}
	if tune != nil {
		tune(&cfg)
	}
	startWingdCfg(t, cfg)
	sock, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatalf("api socket path: %v", err)
	}
	return sock, runs
}

func seedRun(t *testing.T, c *client.Client, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := c.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := c.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := c.StartNode(ctx, runID, nodeID); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
}
