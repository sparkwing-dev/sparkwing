package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const wingdTestWait = 30 * time.Second

func wingdTestHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swe2e")
	if err != nil {
		dir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	return dir
}

type stubSampler struct{ stat wingd.HostStat }

func (s stubSampler) Sample() (wingd.HostStat, error) { return s.stat, nil }

func startWingd(t *testing.T, home string, cores float64) {
	t.Helper()
	startWingdCfg(t, wingd.Config{
		Home:    home,
		Version: "test",
		Sampler: stubSampler{wingd.HostStat{
			TotalCores:       cores,
			TotalMemoryBytes: 64 << 30,
			FreeMemoryBytes:  64 << 30,
			LoadMeasured:     true,
			MemoryMeasured:   true,
		}},
		HeadroomFraction: -1,
		GraceWindow:      -1,
	})
}

func startWingdCfg(t *testing.T, cfg wingd.Config) {
	t.Helper()
	if cfg.StoreSchemaVersion == 0 {
		cfg.StoreSchemaVersion = store.ExpectedSchemaVersion()
	}
	if cfg.Runs == nil {
		_, st, _ := openWingdBackends(t, cfg.Home)
		ctx := context.WithoutCancel(t.Context())
		cfg.Runs = &wingd.FuncRunStore{
			IsTerminal: func(id string) (bool, error) {
				run, err := st.GetRun(ctx, id)
				if errors.Is(err, store.ErrNotFound) {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				return (run.Status == "success" || run.Status == "failed" || run.Status == "cancelled"), nil
			},
			Finalize: func(id string) {
				if err := st.FinishRunsIfActive(ctx, []string{id}, "cancelled", "admission connection lost"); err != nil {
					t.Errorf("finalize run: %v", err)
				}
			},
			FinalizeCancelled: func(ids []string, reason string) error {
				return st.FinishRunsIfActive(ctx, ids, "cancelled", reason)
			},
		}
	}
	d, err := wingd.New(cfg)
	if err != nil {
		t.Fatalf("wingd.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(wingdTestWait):
			t.Error("daemon did not stop")
		}
	})
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(wingdTestWait):
		t.Fatal("daemon did not become ready")
	}
}

func testWingdAdmission(home string, out io.Writer) *LocalAdmission {
	if out == nil {
		out = io.Discard
	}
	return &LocalAdmission{
		Home:    home,
		Version: "test",
		Out:     out,
		Spawn:   func(string, string) error { return errors.New("no daemon running for test home") },
	}
}

func openWingdBackends(t *testing.T, home string) (Backends, *store.Store, Paths) {
	t.Helper()
	p := PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return LocalBackends(p, st), st, p
}

type wingdGate struct {
	started chan string
	release chan struct{}
	running atomic.Int32
	peak    atomic.Int32
}

func (g *wingdGate) run(ctx context.Context, runID string) error {
	n := g.running.Add(1)
	for {
		peak := g.peak.Load()
		if n <= peak || g.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer g.running.Add(-1)
	g.started <- runID
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func queryWingd(t *testing.T, home string) wingwire.QueueState {
	t.Helper()
	qs, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("queue query: %v", err)
	}
	return qs
}

func waitForWingdPoll(poll *time.Ticker) {
	poll.Reset(10 * time.Millisecond)
	<-poll.C
}

func awaitWaiter(t *testing.T, home, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wingdTestWait)
	defer cancel()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		for _, w := range queryWingd(t, home).Waiters {
			if queueRowMatchesRun(w.RunID, w.ParticipantID, runID) {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %q never appeared in the daemon queue", runID)
		case <-poll.C:
		}
	}
}

func queueRowMatchesRun(runID, participantID, want string) bool {
	return runID == want || participantID == want
}

func TestWingd_DaemonFirstCancelSurvivesImmediateClientExit(t *testing.T) {
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	_, st, _ := openWingdBackends(t, home)
	ctx := context.Background()
	const runID = "cancel-exit-holder"

	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "wingd-e2e-hold", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run row: %v", err)
	}
	holder, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("dial holder client: %v", err)
	}
	if _, err := holder.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: runID, Resources: wingwire.HostResources{Cores: 1},
	}, nil); err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	found, err := wingdclient.Cancel(ctx, wingdclient.Options{Home: home, Version: "test"}, runID)
	if err != nil {
		t.Fatalf("daemon-first cancel: %v", err)
	}
	if !found {
		t.Fatal("daemon did not find holder")
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close holder immediately after cancel: %v", err)
	}

	deadline := time.Now().Add(wingdTestWait)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		run, getErr := st.GetRun(ctx, runID)
		if getErr != nil {
			t.Fatalf("get run: %v", getErr)
		}
		if run.Status == "cancelled" {
			if run.Error != "cancelled via sparkwing runs cancel" {
				t.Fatalf("stored cancellation = %q, want explicit operator attribution", run.Error)
			}
			return
		}
		waitForWingdPoll(poll)
	}
	t.Fatal("cancelled holder never finalized")
}

func TestWingd_DaemonFirstCancelFinalizesQueuedClient(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	_, st, _ := openWingdBackends(t, home)
	ctx := context.Background()
	const runID = "cancel-exit-waiter"

	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "wingd-e2e-hold", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run row: %v", err)
	}
	holder, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("dial capacity holder: %v", err)
	}
	holderLease, err := holder.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: "cancel-waiter-blocker", Resources: wingwire.HostResources{Cores: 2},
	}, nil)
	if err != nil {
		t.Fatalf("acquire capacity holder: %v", err)
	}
	defer func() { _ = holderLease.Release() }()

	waiter, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("dial waiter client: %v", err)
	}
	waitResult := make(chan error, 1)
	go func() {
		_, acquireErr := waiter.Acquire(ctx, wingwire.AdmissionRequest{
			RunID: runID, Resources: wingwire.HostResources{Cores: 1},
		}, nil)
		waitResult <- acquireErr
	}()
	awaitWaiter(t, home, runID)

	found, err := wingdclient.Cancel(ctx, wingdclient.Options{Home: home, Version: "test"}, runID)
	if err != nil {
		t.Fatalf("daemon-first cancel: %v", err)
	}
	if !found {
		t.Fatal("daemon did not find waiter")
	}
	select {
	case <-waitResult:
	case <-time.After(wingdTestWait):
		t.Fatal("waiter did not receive cancellation")
	}
	_ = waiter.Close()

	deadline := time.Now().Add(wingdTestWait)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		run, getErr := st.GetRun(ctx, runID)
		if getErr != nil {
			t.Fatalf("get run: %v", getErr)
		}
		if run.Status == "cancelled" {
			if run.Error != "cancelled via sparkwing runs cancel" {
				t.Fatalf("stored cancellation = %q, want explicit operator attribution", run.Error)
			}
			return
		}
		waitForWingdPoll(poll)
	}
	t.Fatal("cancelled waiter never finalized")
}
