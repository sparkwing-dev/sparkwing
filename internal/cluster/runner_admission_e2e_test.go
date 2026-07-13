package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// fetchAgents reads GET /api/v1/agents and keys the result by agent name.
func fetchAgents(t *testing.T, baseURL string) map[string]controller.Agent {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("get agents: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Agents []controller.Agent `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	out := map[string]controller.Agent{}
	for _, a := range body.Agents {
		out[a.Name] = a
	}
	return out
}

// e2eSampler feeds the daemon a fixed machine size so capacity is
// deterministic across platforms.
type e2eSampler struct{ cores float64 }

func (s e2eSampler) Sample() (wingd.HostStat, error) {
	return wingd.HostStat{TotalCores: s.cores, TotalMemoryBytes: 16 << 30, FreeMemoryBytes: 16 << 30}, nil
}

func startE2EDaemon(t *testing.T, home string, cores float64) {
	t.Helper()
	d, err := wingd.New(wingd.Config{
		Home: home, Version: "v1", GraceWindow: -1,
		HeadroomFraction: -1,
		Sampler:          e2eSampler{cores: cores},
	})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = d.Run(ctx) }()
	select {
	case <-d.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("daemon never became ready")
	}
}

func e2eNoSpawn(string, string) error { return wingdclient.ErrNoDaemon }

// holdLease acquires and holds a host-cores lease for the run's lifetime,
// standing in for a run of that origin holding capacity on the box. The
// returned client must stay open to hold the lease; it is closed on
// cleanup.
func holdLease(t *testing.T, home, runID string, cores float64, origin wingwire.Origin) {
	t.Helper()
	cl, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{
		Home: home, Version: "v1", Spawn: e2eNoSpawn, DialTimeout: time.Second, Backoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	if _, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID: runID, Pipeline: runID, Origin: origin,
		Resources: wingwire.HostResources{Cores: cores},
	}, nil); err != nil {
		t.Fatalf("hold %s: %v", runID, err)
	}
}

// TestRunnerAdmissionE2E stands up a real local admission daemon and a real
// controller, holds local and controller-origin work on the box, and
// asserts the two behaviors this machine can show:
// controller-dispatched work appears in the queue with a controller origin
// beside a local run, and the headroom the runner advertises to the
// controller shrinks as local work holds capacity.
func TestRunnerAdmissionE2E(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sw-e2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	const machineCores = 8
	startE2EDaemon(t, home, machineCores)

	st, err := store.Open(home + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	ctrl := client.New(srv.URL, nil)
	ctx := context.Background()

	rv := reserve{cores: 1}
	provider := newHeadroomProvider(home, rv)

	idle := provider(ctx)
	if idle == nil {
		t.Fatal("provider returned nil against a live daemon")
	}
	if idle.Cores < machineCores-rv.cores-0.5 {
		t.Fatalf("idle advertised cores = %v, want ~%v", idle.Cores, machineCores-rv.cores)
	}

	holdLease(t, home, "local-run", 4, wingwire.OriginLocal)
	holdLease(t, home, "ctrl-run", 1, wingwire.OriginController)

	held := provider(ctx)
	if held == nil {
		t.Fatal("provider returned nil while leases held")
	}
	if !(held.Cores < idle.Cores) {
		t.Fatalf("advertised cores should shrink under load: idle=%v held=%v", idle.Cores, held.Cores)
	}
	wantHeld := machineCores - 4 - 1 - rv.cores
	if held.Cores < float64(wantHeld)-0.5 || held.Cores > float64(wantHeld)+0.5 {
		t.Errorf("advertised cores = %v, want ~%v (machine %d - held 5 - reserve 1)", held.Cores, wantHeld, machineCores)
	}

	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatalf("query queue: %v", err)
	}
	origins := map[string]wingwire.Origin{}
	for _, h := range qs.Holders {
		origins[h.RunID] = h.Origin
	}
	if origins["local-run"] != wingwire.OriginLocal {
		t.Errorf("local-run origin = %q, want local", origins["local-run"])
	}
	if origins["ctrl-run"] != wingwire.OriginController {
		t.Errorf("ctrl-run origin = %q, want controller", origins["ctrl-run"])
	}

	if err := st.CreateRun(ctx, store.Run{ID: "cwork", Pipeline: "deploy", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "cwork", NodeID: "n1", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "cwork", "n1"); err != nil {
		t.Fatal(err)
	}
	claimed, err := ctrl.ClaimNode(ctx, "runner:boxA:1", nil, 30*time.Second, held)
	if err != nil {
		t.Fatalf("claim node: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected to claim the ready node")
	}
	agents := fetchAgents(t, srv.URL)
	box, ok := agents["boxA"]
	if !ok {
		t.Fatalf("boxA not in agents view: %+v", agents)
	}
	if box.Headroom == nil {
		t.Fatal("agents view did not surface advertised headroom")
	}
	if box.Headroom.Cores != held.Cores {
		t.Errorf("surfaced headroom cores = %v, want %v", box.Headroom.Cores, held.Cores)
	}
}
