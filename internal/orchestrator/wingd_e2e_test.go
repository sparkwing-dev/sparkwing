package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const wingdTestWait = 30 * time.Second

// wingdTestHome returns a scratch sparkwing home under /tmp so the
// daemon's unix socket path stays within the OS length limit.
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

// idleProcSampler reports every holder process as consuming no CPU, so
// stall flagging fires deterministically for a blocked holder.
type idleProcSampler struct{}

func (idleProcSampler) CPUUsage(int) (wingd.ProcUsage, bool) { return wingd.ProcUsage{}, true }

// startWingd runs a real daemon in-process for home with a fixed host
// capacity, wired to the same orphan-run finalizer production uses.
func startWingd(t *testing.T, home string, cores float64) {
	t.Helper()
	startWingdCfg(t, wingd.Config{
		Home:    home,
		Version: "test",
		Sampler: stubSampler{wingd.HostStat{
			TotalCores:       cores,
			TotalMemoryBytes: 64 << 30,
			FreeMemoryBytes:  64 << 30,
		}},
		HeadroomFraction: -1,
		GraceWindow:      -1,
		FinalizeRun:      NewOrphanRunFinalizer(home),
	})
}

// startWingdCfg runs a daemon in-process for the given config, ensuring
// the orphan finalizer is wired and cleaning it up at test end.
func startWingdCfg(t *testing.T, cfg wingd.Config) {
	t.Helper()
	if cfg.FinalizeRun == nil {
		cfg.FinalizeRun = NewOrphanRunFinalizer(cfg.Home)
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

func testWingdAdmission(home string, stderr io.Writer) *LocalAdmission {
	if stderr == nil {
		stderr = io.Discard
	}
	return &LocalAdmission{
		Home:    home,
		Version: "test",
		Stderr:  stderr,
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
	return LocalBackends(p, st, nil), st, p
}

// wingdGate coordinates test pipelines: nodes report they started and
// block until released (or their context ends).
type wingdGate struct {
	started chan string
	release chan struct{}
	running atomic.Int32
	peak    atomic.Int32
}

func newWingdGate() *wingdGate {
	return &wingdGate{started: make(chan string, 16), release: make(chan struct{})}
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

func (g *wingdGate) awaitStarted(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-g.started:
		if got != want {
			t.Fatalf("started run = %q, want %q", got, want)
		}
	case <-time.After(wingdTestWait):
		t.Fatalf("run %q never started", want)
	}
}

var (
	wingdE2EGate     atomic.Pointer[wingdGate]
	wingdE2EChild    atomic.Pointer[wingdChildLaunch]
	wingdE2ERegister sync.Once
)

type wingdChildLaunch struct {
	home     string
	backends Backends
	result   chan *Result
}

type wingdHoldPipe struct {
	sparkwing.Base
	cores float64
}

func (p wingdHoldPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(p.cores))
	runID := rc.RunID
	sparkwing.Job(plan, "hold", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	})
	return nil
}

// wingdUnpinnedHoldPipe declares no .Resources() pin, so admission
// resolves its cost from measurement (or the cold-start default).
type wingdUnpinnedHoldPipe struct{ sparkwing.Base }

func (wingdUnpinnedHoldPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	runID := rc.RunID
	sparkwing.Job(plan, "hold", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	})
	return nil
}

type wingdQuickPipe struct{ sparkwing.Base }

func (wingdQuickPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(0.5))
	sparkwing.Job(plan, "quick", func(context.Context) error { return nil })
	return nil
}

type wingdAttachParentPipe struct{ sparkwing.Base }

func (wingdAttachParentPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(2))
	sparkwing.Job(plan, "spawn-child", func(ctx context.Context) error {
		la, token, _ := localAdmissionFromContext(ctx)
		if la == nil || token == "" {
			return errors.New("node context carries no local admission lease")
		}
		launch := wingdE2EChild.Load()
		childCtx, cancel := context.WithTimeout(ctx, wingdTestWait)
		defer cancel()
		res, err := Run(childCtx, launch.backends, Options{
			Pipeline: "wingd-e2e-quick",
			RunID:    "wingd-attach-child",
			Admission: &LocalAdmission{
				Home:             launch.home,
				Version:          "test",
				ParentLeaseToken: token,
				Stderr:           io.Discard,
				Spawn:            func(string, string) error { return errors.New("no daemon running for test home") },
			},
		})
		if err != nil {
			return fmt.Errorf("child run: %w", err)
		}
		launch.result <- res
		if res.Status != "success" {
			return fmt.Errorf("child status %q: %v", res.Status, res.Error)
		}
		return nil
	})
	return nil
}

func wingdDeployGroup(onLimit sparkwing.OnLimit) *sparkwing.ConcurrencyGroup {
	return sparkwing.NewConcurrencyGroup("wingd-e2e-deploy", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		Scope:    sparkwing.ScopeBox,
		OnLimit:  onLimit,
	})
}

type wingdEvictVictimPipe struct{ sparkwing.Base }

func (wingdEvictVictimPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1))
	plan.Concurrency(wingdDeployGroup(sparkwing.Queue))
	runID := rc.RunID
	sparkwing.Job(plan, "hold", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	})
	return nil
}

type wingdEvictAggressorPipe struct{ sparkwing.Base }

func (wingdEvictAggressorPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1))
	plan.Concurrency(wingdDeployGroup(sparkwing.CancelOthers))
	sparkwing.Job(plan, "deploy", func(context.Context) error { return nil })
	return nil
}

type wingdNodeSemPipe struct{ sparkwing.Base }

func (wingdNodeSemPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1))
	group := sparkwing.NewConcurrencyGroup("wingd-e2e-node-lock", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		Scope:    sparkwing.ScopeBox,
		OnLimit:  sparkwing.Queue,
	})
	runID := rc.RunID
	sparkwing.Job(plan, "locked", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	}).Concurrency(group)
	return nil
}

type wingdPlanSemUnpinnedPipe struct{ sparkwing.Base }

func (wingdPlanSemUnpinnedPipe) Plan(
	_ context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(wingdDeployGroup(sparkwing.Queue))
	runID := rc.RunID
	sparkwing.Job(plan, "hold", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	})
	return nil
}

type wingdPlanSemSpawnChildPipe struct{ sparkwing.Base }

func (wingdPlanSemSpawnChildPipe) Plan(
	_ context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	_ sparkwing.RunContext,
) error {
	plan.Concurrency(wingdDeployGroup(sparkwing.Queue))
	sparkwing.Job(plan, "spawn-child", func(ctx context.Context) error {
		env := leaseTriggerEnv(ctx)
		token := env[wingwire.LeaseTokenEnv]
		if token == "" {
			return errors.New("child trigger token missing")
		}
		_, currentToken, _ := localAdmissionFromContext(ctx)
		if currentToken == "" {
			return errors.New("current node token missing")
		}
		if currentToken == token {
			return errors.New("node host token must differ from child trigger token")
		}
		out, err := sparkwing.Exec(ctx, "sh", "-c",
			`printf '%s/%s' "$SPARKWING_LEASE_TOKEN" "$SPARKWING_CHILD_LEASE_TOKEN"`).String()
		if err != nil {
			return err
		}
		if out != currentToken+"/"+token {
			return fmt.Errorf("command env = %q, want current and child tokens", out)
		}
		launch := wingdE2EChild.Load()
		childCtx, cancel := context.WithTimeout(ctx, wingdTestWait)
		defer cancel()
		res, err := Run(childCtx, launch.backends, Options{
			Pipeline: "wingd-e2e-plan-sem-child",
			RunID:    "wingd-plan-sem-child",
			Admission: &LocalAdmission{
				Home:             launch.home,
				Version:          "test",
				ParentLeaseToken: token,
				Stderr:           io.Discard,
				Spawn:            func(string, string) error { return errors.New("no daemon running for test home") },
			},
		})
		if err != nil {
			return fmt.Errorf("child run: %w", err)
		}
		launch.result <- res
		if res.Status != "success" {
			return fmt.Errorf("child status %q: %v", res.Status, res.Error)
		}
		return nil
	})
	return nil
}

type wingdPlanSemChildPipe struct{ sparkwing.Base }

func (wingdPlanSemChildPipe) Plan(
	_ context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	_ sparkwing.RunContext,
) error {
	plan.Concurrency(wingdDeployGroup(sparkwing.Queue))
	sparkwing.Job(plan, "work", func(context.Context) error { return nil })
	return nil
}

type wingdCachedUnpinnedPipe struct{ sparkwing.Base }

func (wingdCachedUnpinnedPipe) Plan(
	_ context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	runID := rc.RunID
	sparkwing.Job(plan, "cached", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	}).Cache(func(context.Context) sparkwing.CacheKey {
		return sparkwing.Key("wingd-e2e-cached", "stable")
	})
	return nil
}

func wingdNodeEvictGroup(onLimit sparkwing.OnLimit) *sparkwing.ConcurrencyGroup {
	return sparkwing.NewConcurrencyGroup("wingd-e2e-node-evict", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		Scope:         sparkwing.ScopeBox,
		OnLimit:       onLimit,
		CancelTimeout: 5 * time.Second,
	})
}

type wingdNodeEvictVictimPipe struct{ sparkwing.Base }

func (wingdNodeEvictVictimPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1))
	runID := rc.RunID
	sparkwing.Job(plan, "locked", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	}).Concurrency(wingdNodeEvictGroup(sparkwing.CancelOthers))
	return nil
}

type wingdNodeEvictAggressorPipe struct{ sparkwing.Base }

func (wingdNodeEvictAggressorPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1))
	sparkwing.Job(plan, "locked", func(context.Context) error { return nil }).
		Concurrency(wingdNodeEvictGroup(sparkwing.CancelOthers))
	return nil
}

type wingdProfiledStagePipe struct{ sparkwing.Base }

func (wingdProfiledStagePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	quick := sparkwing.Job(plan, "quick", func(context.Context) error { return nil })
	runID := rc.RunID
	sparkwing.Job(plan, "heavy", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	}).Needs(quick)
	return nil
}

type wingdRecoveryUnpinnedPipe struct{ sparkwing.Base }

func (wingdRecoveryUnpinnedPipe) Plan(
	_ context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	runID := rc.RunID
	sparkwing.Job(plan, "fail", func(context.Context) error {
		return errors.New("failed before recovery")
	}).OnFailure("recover", func(ctx context.Context) error {
		return wingdE2EGate.Load().run(ctx, runID)
	})
	return nil
}

func registerWingdE2EPipelines() {
	wingdE2ERegister.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-hold",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdHoldPipe{cores: 1.5} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-quick",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdQuickPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-unpinned",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdUnpinnedHoldPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-attach-parent",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdAttachParentPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-evict-victim",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdEvictVictimPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-evict-aggressor",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdEvictAggressorPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-node-sem",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdNodeSemPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-plan-sem-unpinned",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdPlanSemUnpinnedPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-plan-sem-spawn-child",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdPlanSemSpawnChildPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-plan-sem-child",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdPlanSemChildPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-cached-unpinned",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdCachedUnpinnedPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-node-evict-victim",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdNodeEvictVictimPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-node-evict-aggressor",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdNodeEvictAggressorPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-profiled-stage",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdProfiledStagePipe{} })
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-recovery-unpinned",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdRecoveryUnpinnedPipe{} })
	})
}

func queryWingd(t *testing.T, home string) wingwire.QueueState {
	t.Helper()
	qs, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("queue query: %v", err)
	}
	return qs
}

func awaitWaiter(t *testing.T, home, runID string) {
	t.Helper()
	deadline := time.Now().Add(wingdTestWait)
	for time.Now().Before(deadline) {
		for _, w := range queryWingd(t, home).Waiters {
			if queueRowMatchesRun(w.RunID, w.ParticipantID, runID) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %q never appeared in the daemon queue", runID)
}

func awaitWaiterOrHolder(t *testing.T, home, runID string) wingwire.QueueState {
	t.Helper()
	deadline := time.Now().Add(wingdTestWait)
	var last wingwire.QueueState
	for time.Now().Before(deadline) {
		last = queryWingd(t, home)
		for _, w := range last.Waiters {
			if queueRowMatchesRun(w.RunID, w.ParticipantID, runID) {
				return last
			}
		}
		for _, h := range last.Holders {
			if queueRowMatchesRun(h.RunID, h.ParticipantID, runID) {
				t.Fatalf("run %q was admitted as holder; queue state: %+v", runID, last)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %q never appeared as waiter or holder; queue state: %+v", runID, last)
	return wingwire.QueueState{}
}

func acquireWingd(t *testing.T, cl *wingdclient.Client, req wingwire.AdmissionRequest) *wingdclient.Lease {
	t.Helper()
	lease, err := cl.Acquire(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("acquire %q: %v", req.RunID, err)
	}
	return lease
}

func awaitNodeOutcome(t *testing.T, st *store.Store, runID, nodeID, outcome string) {
	t.Helper()
	deadline := time.Now().Add(wingdTestWait)
	var last string
	for time.Now().Before(deadline) {
		node, err := st.GetNode(context.Background(), runID, nodeID)
		if err == nil && node != nil {
			last = node.Outcome
			if node.Outcome == outcome {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s/%s outcome %q; last outcome %q", runID, nodeID, outcome, last)
}

func findWingdHolder(t *testing.T, home, runID string) wingwire.Holder {
	t.Helper()
	deadline := time.Now().Add(wingdTestWait)
	for time.Now().Before(deadline) {
		for _, h := range queryWingd(t, home).Holders {
			if queueRowMatchesRun(h.RunID, h.ParticipantID, runID) {
				return h
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %q never appeared as a holder", runID)
	return wingwire.Holder{}
}

func hasWingdHolder(qs wingwire.QueueState, runID string) bool {
	for _, h := range qs.Holders {
		if queueRowMatchesRun(h.RunID, h.ParticipantID, runID) {
			return true
		}
	}
	return false
}

func queueRowMatchesRun(runID, participantID, want string) bool {
	return runID == want || participantID == want
}

// findQueuedWaiter returns the queued waiter with runID and whether it is
// present, without blocking.
func findQueuedWaiter(qs wingwire.QueueState, runID string) (wingwire.Waiter, bool) {
	for _, w := range qs.Waiters {
		if queueRowMatchesRun(w.RunID, w.ParticipantID, runID) {
			return w, true
		}
	}
	return wingwire.Waiter{}, false
}

func seedProfile(t *testing.T, st *store.Store, pipeline string, obs store.ProfileObservation, runs int) {
	t.Helper()
	seedNodeProfile(t, st, pipeline, "", obs, runs)
}

func seedNodeProfile(t *testing.T, st *store.Store, pipeline, nodeID string, obs store.ProfileObservation, runs int) {
	t.Helper()
	for range runs {
		if err := st.RecordProfileObservation(context.Background(), pipeline, nodeID, obs); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
	}
}

func TestWingd_SecondRunAdmittedWithMeasuredCost(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)
	seedNodeProfile(t, st, "wingd-e2e-unpinned", "hold", store.ProfileObservation{
		Duration: 20 * time.Second, PeakCores: 1.5, PeakMemoryBytes: 1 << 30,
	}, 3)

	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-unpinned",
			RunID:     "measured-run",
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()
	gate.awaitStarted(t, "measured-run")

	h := findWingdHolder(t, home, nodeHostRunID("measured-run", "hold"))
	if h.CostSource != "measured" {
		t.Errorf("CostSource = %q, want measured", h.CostSource)
	}
	if h.Resources.Cores != 1.5 {
		t.Errorf("admitted cores = %v, want the measured peak 1.5", h.Resources.Cores)
	}
	if h.ExpectedDurationMS != (20 * time.Second).Milliseconds() {
		t.Errorf("ExpectedDurationMS = %d, want 20000", h.ExpectedDurationMS)
	}
	if qs := queryWingd(t, home); qs.ExpectedClearMS == nil {
		t.Error("ExpectedClearMS is nil; a measured holder should yield a clear estimate")
	}

	close(gate.release)
	select {
	case res := <-done:
		if res == nil || res.Status != "success" {
			t.Fatalf("run result = %+v, want success", res)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("run did not finish")
	}
}

// TestWingd_ZeroCPUPipelineAdmitsAtTinyMeasuredCostAlongsideHeavyWork
// seeds a sleep-heavy pipeline with three healthy-sampler observations at
// a zero CPU peak. On the 2-core box its cold-start default (1 core) would
// not fit behind the 1.5-core holder, so admitting it concurrently proves
// the zero-CPU profile is trusted at its tiny measured cost.
func TestWingd_ZeroCPUPipelineAdmitsAtTinyMeasuredCostAlongsideHeavyWork(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	backends, st, _ := openWingdBackends(t, home)
	seedNodeProfile(t, st, "wingd-e2e-unpinned", "hold", store.ProfileObservation{
		Duration: 5 * time.Second, PeakCores: 0, PeakMemoryBytes: 64 << 20, CPUMeasured: true,
	}, capacity.MinSamples)

	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	ctx := context.Background()

	heavy := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "heavy-holder",
			Admission: testWingdAdmission(home, nil),
		})
		heavy <- res
	}()
	gate.awaitStarted(t, "heavy-holder")

	sleepy := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-unpinned",
			RunID:     "sleepy-run",
			Admission: testWingdAdmission(home, nil),
		})
		sleepy <- res
	}()
	gate.awaitStarted(t, "sleepy-run")

	h := findWingdHolder(t, home, nodeHostRunID("sleepy-run", "hold"))
	if h.CostSource != "measured" {
		t.Errorf("CostSource = %q, want measured (healthy-sampler zero-CPU profile)", h.CostSource)
	}
	if h.Resources.Cores != 0.1 {
		t.Errorf("admitted cores = %v, want the 0.1 measured core floor", h.Resources.Cores)
	}
	qs := queryWingd(t, home)
	if !hasWingdHolder(qs, "heavy-holder") || !hasWingdHolder(qs, nodeHostRunID("sleepy-run", "hold")) {
		t.Fatalf("holders = %+v, want heavy work and the sleep-heavy node concurrently", qs.Holders)
	}

	close(gate.release)
	for name, ch := range map[string]chan *Result{"heavy-holder": heavy, "sleepy-run": sleepy} {
		select {
		case res := <-ch:
			if res == nil || res.Status != "success" {
				t.Fatalf("%s result = %+v, want success", name, res)
			}
		case <-time.After(wingdTestWait):
			t.Fatalf("%s did not finish", name)
		}
	}
}

func TestWingd_UnderPinnedRunCarriesDriftWarning(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)
	seedProfile(t, st, "wingd-e2e-hold", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 9, PeakMemoryBytes: 1 << 30,
	}, 4)

	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "drift-run",
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()
	gate.awaitStarted(t, "drift-run")

	h := findWingdHolder(t, home, "drift-run")
	if h.CostSource != "pin" {
		t.Errorf("CostSource = %q, want pin (the pin is authoritative)", h.CostSource)
	}
	if h.Resources.Cores != 1.5 {
		t.Errorf("admitted cores = %v, want the pinned 1.5", h.Resources.Cores)
	}
	if !strings.Contains(h.DriftWarning, "resource pin") || !strings.Contains(h.DriftWarning, "measured p99") {
		t.Errorf("DriftWarning = %q, want the under-pinned note with the exact fix", h.DriftWarning)
	}

	close(gate.release)
	<-done
}

func TestWingd_SecondRunQueuesUntilFirstReleases(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	backends, st, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	runA := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-q-a",
			Admission: testWingdAdmission(home, nil),
		})
		runA <- res
	}()
	gate.awaitStarted(t, "wingd-q-a")

	var stderrB strings.Builder
	runB := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-q-b",
			Admission: testWingdAdmission(home, &stderrB),
		})
		runB <- res
	}()
	awaitWaiter(t, home, "wingd-q-b")

	if node, err := st.GetNode(context.Background(), "wingd-q-b", "hold"); err == nil && node.Status != "pending" {
		t.Fatalf("queued run's node status = %q, want pending while waiting for admission", node.Status)
	}

	close(gate.release)
	for _, ch := range []chan *Result{runA, runB} {
		select {
		case res := <-ch:
			if res == nil || res.Status != "success" {
				t.Fatalf("run result = %+v, want success", res)
			}
		case <-time.After(wingdTestWait):
			t.Fatal("run did not finish after release")
		}
	}
	if !strings.Contains(stderrB.String(), "queued for local admission") {
		t.Fatalf("queued run stderr = %q, want a queue-position line", stderrB.String())
	}
	if got := gate.peak.Load(); got != 1 {
		t.Fatalf("peak concurrent holds = %d, want host capacity to admit one at a time", got)
	}
}

func TestWingd_LocalRunAdmitsReadyNodeAtNodeCost(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 10)
	backends, st, _ := openWingdBackends(t, home)
	ctx := context.Background()

	seedProfile(t, st, "wingd-e2e-profiled-stage", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 8, PeakMemoryBytes: 16 << 30,
	}, 4)
	seedNodeProfile(t, st, "wingd-e2e-profiled-stage", "quick", store.ProfileObservation{
		Duration: 100 * time.Millisecond, PeakCores: 0.1, PeakMemoryBytes: 64 << 20,
		CPUMeasured: true,
	}, 4)
	seedNodeProfile(t, st, "wingd-e2e-profiled-stage", "heavy", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 8, PeakMemoryBytes: 16 << 30,
	}, 4)

	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("daemon client: %v", err)
	}
	holder := acquireWingd(t, cl, wingwire.AdmissionRequest{
		RunID:     "external-heavy-holder",
		Pipeline:  "external-heavy-holder",
		Resources: wingwire.HostResources{MemoryBytes: 56 << 30},
	})
	t.Cleanup(func() { _ = holder.Release() })

	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-profiled-stage",
			RunID:     "profiled-stage-run",
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()

	awaitNodeOutcome(t, st, "profiled-stage-run", "quick", string(sparkwing.Success))
	heavyHostID := nodeHostRunID("profiled-stage-run", "heavy")
	qs := awaitWaiterOrHolder(t, home, heavyHostID)
	if w, ok := findQueuedWaiter(qs, heavyHostID); !ok || w.Position != 1 {
		t.Fatalf("heavy stage waiter position = %d (present=%v), want queued behind external holder", w.Position, ok)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	gate.awaitStarted(t, "profiled-stage-run")
	close(gate.release)

	select {
	case res := <-done:
		if res == nil || res.Status != "success" {
			t.Fatalf("run result = %+v, want success", res)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("run did not finish")
	}
}

func TestWingd_SemaphoresOnlyRunStillAdmitsNodeHostCost(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 10)
	backends, st, _ := openWingdBackends(t, home)
	ctx := context.Background()

	seedNodeProfile(t, st, "wingd-e2e-plan-sem-unpinned", "hold", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 1, PeakMemoryBytes: 16 << 30,
	}, 4)

	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("daemon client: %v", err)
	}
	holder := acquireWingd(t, cl, wingwire.AdmissionRequest{
		RunID:     "external-plan-sem-holder",
		Pipeline:  "external-plan-sem-holder",
		Resources: wingwire.HostResources{MemoryBytes: 56 << 30},
	})
	t.Cleanup(func() { _ = holder.Release() })

	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-plan-sem-unpinned",
			RunID:     "sem-node-run",
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()

	semNodeHostID := nodeHostRunID("sem-node-run", "hold")
	qs := awaitWaiterOrHolder(t, home, semNodeHostID)
	if w, ok := findQueuedWaiter(qs, semNodeHostID); !ok || w.Position != 1 {
		t.Fatalf("node host waiter position = %d (present=%v), want queued behind external holder", w.Position, ok)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	gate.awaitStarted(t, "sem-node-run")
	close(gate.release)

	select {
	case res := <-done:
		if res == nil || res.Status != "success" {
			t.Fatalf("run result = %+v, want success", res)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("run did not finish")
	}
}

func TestWingd_RecoveryNodeAdmitsHostCost(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 10)
	backends, st, _ := openWingdBackends(t, home)
	ctx := context.Background()

	seedNodeProfile(t, st, "wingd-e2e-recovery-unpinned", "recover", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 1, PeakMemoryBytes: 16 << 30,
	}, 4)

	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("daemon client: %v", err)
	}
	holder := acquireWingd(t, cl, wingwire.AdmissionRequest{
		RunID:     "external-recovery-holder",
		Pipeline:  "external-recovery-holder",
		Resources: wingwire.HostResources{MemoryBytes: 56 << 30},
	})
	t.Cleanup(func() { _ = holder.Release() })

	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-recovery-unpinned",
			RunID:     "recovery-node-run",
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()

	recoverHostID := nodeHostRunID("recovery-node-run", "recover")
	qs := awaitWaiterOrHolder(t, home, recoverHostID)
	if w, ok := findQueuedWaiter(qs, recoverHostID); !ok || w.Position != 1 {
		t.Fatalf("recovery host waiter position = %d (present=%v), want queued behind external holder", w.Position, ok)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	gate.awaitStarted(t, "recovery-node-run")
	close(gate.release)

	select {
	case res := <-done:
		if res == nil || res.Status != "failed" {
			t.Fatalf("run result = %+v, want failed parent with successful recovery", res)
		}
		node, err := st.GetNode(ctx, "recovery-node-run", "recover")
		if err != nil {
			t.Fatalf("get recovery node: %v", err)
		}
		if node.Outcome != string(sparkwing.Success) {
			t.Fatalf("recovery outcome = %q, want success", node.Outcome)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("run did not finish")
	}
}

func TestWingd_ChildTriggerInheritsRunSemaphoreWhileNodeHasHostLease(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 10)
	backends, st, _ := openWingdBackends(t, home)
	launch := &wingdChildLaunch{home: home, backends: backends, result: make(chan *Result, 1)}
	wingdE2EChild.Store(launch)

	seedNodeProfile(t, st, "wingd-e2e-plan-sem-spawn-child", "spawn-child", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 1, PeakMemoryBytes: 16 << 30,
	}, 4)

	res, err := Run(context.Background(), backends, Options{
		Pipeline:  "wingd-e2e-plan-sem-spawn-child",
		RunID:     "sem-spawn-parent",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil {
		t.Fatalf("parent run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("parent status = %q (err=%v), want child to inherit the held plan semaphore", res.Status, res.Error)
	}
	select {
	case child := <-launch.result:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success", child.Status)
		}
	default:
		t.Fatal("child run never reported a result")
	}
}

func TestWingd_CachedNodeMissAdmitsHostCost(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 10)
	backends, st, _ := openWingdBackends(t, home)
	ctx := context.Background()

	seedNodeProfile(t, st, "wingd-e2e-cached-unpinned", "cached", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 1, PeakMemoryBytes: 16 << 30,
	}, 4)

	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("daemon client: %v", err)
	}
	holder := acquireWingd(t, cl, wingwire.AdmissionRequest{
		RunID:     "external-cache-holder",
		Pipeline:  "external-cache-holder",
		Resources: wingwire.HostResources{MemoryBytes: 56 << 30},
	})
	t.Cleanup(func() { _ = holder.Release() })

	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-cached-unpinned",
			RunID:     "cached-node-run",
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()

	cachedHostID := nodeHostRunID("cached-node-run", "cached")
	qs := awaitWaiterOrHolder(t, home, cachedHostID)
	if w, ok := findQueuedWaiter(qs, cachedHostID); !ok || w.Position != 1 {
		t.Fatalf("cached node waiter position = %d (present=%v), want queued behind external holder", w.Position, ok)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	gate.awaitStarted(t, "cached-node-run")
	close(gate.release)

	select {
	case res := <-done:
		if res == nil || res.Status != "success" {
			t.Fatalf("run result = %+v, want success", res)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("run did not finish")
	}
}

// syncBuffer is a concurrency-safe stderr sink so a test can poll the
// queue output a run goroutine writes while the run is still blocked.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) count(sub string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), sub)
}

func TestWingd_QueuedRunReemitsWaitStatusAndAnnouncesAdmission(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	backends, _, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	runA := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-hb-a",
			Admission: testWingdAdmission(home, nil),
		})
		runA <- res
	}()
	gate.awaitStarted(t, "wingd-hb-a")

	stderrB := &syncBuffer{}
	adm := testWingdAdmission(home, stderrB)
	adm.QueueHeartbeat = 20 * time.Millisecond
	runB := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-hb-b",
			Admission: adm,
		})
		runB <- res
	}()
	awaitWaiter(t, home, "wingd-hb-b")

	deadline := time.Now().Add(wingdTestWait)
	for stderrB.count("still queued for local admission after") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("queued run never re-emitted its wait status; stderr = %q", stderrB.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(gate.release)
	for _, ch := range []chan *Result{runA, runB} {
		select {
		case res := <-ch:
			if res == nil || res.Status != "success" {
				t.Fatalf("run result = %+v, want success", res)
			}
		case <-time.After(wingdTestWait):
			t.Fatal("run did not finish after release")
		}
	}

	out := stderrB.String()
	if !strings.Contains(out, "queued for local admission: position") {
		t.Fatalf("missing the initial queue-position line: %q", out)
	}
	if !strings.Contains(out, "admitted; starting run") {
		t.Fatalf("admitted line must print unconditionally after any wait: %q", out)
	}
}

func TestWingd_ChildRunAttachesToParentLease(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	backends, _, _ := openWingdBackends(t, home)
	launch := &wingdChildLaunch{home: home, backends: backends, result: make(chan *Result, 1)}
	wingdE2EChild.Store(launch)

	res, err := Run(context.Background(), backends, Options{
		Pipeline:  "wingd-e2e-attach-parent",
		RunID:     "wingd-attach-parent",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil {
		t.Fatalf("parent run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("parent status = %q (err=%v); the child must attach instead of queueing behind a full host", res.Status, res.Error)
	}
	select {
	case child := <-launch.result:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success", child.Status)
		}
	default:
		t.Fatal("child run never reported a result")
	}
}

func TestWingd_AbruptDeathReleasesLeaseFinalizesRunAndPromotesNext(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	backends, st, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "wingd-dead", Pipeline: "wingd-e2e-hold", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run row: %v", err)
	}
	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	if _, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID:     "wingd-dead",
		Resources: wingwire.HostResources{Cores: 2},
	}, nil); err != nil {
		t.Fatalf("acquire full capacity: %v", err)
	}

	runB := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-survivor",
			Admission: testWingdAdmission(home, nil),
		})
		runB <- res
	}()
	awaitWaiter(t, home, "wingd-survivor")

	// safety: closing the socket without Release is exactly what the
	// kernel reports when the holder is SIGKILLed.
	_ = cl.Close()

	gate.awaitStarted(t, "wingd-survivor")
	close(gate.release)
	select {
	case res := <-runB:
		if res == nil || res.Status != "success" {
			t.Fatalf("survivor result = %+v, want success after the dead holder's lease was released", res)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("survivor never finished")
	}

	deadline := time.Now().Add(wingdTestWait)
	for {
		run, err := st.GetRun(ctx, "wingd-dead")
		if err != nil {
			t.Fatalf("get dead run: %v", err)
		}
		if run.Status == "cancelled" {
			if !strings.Contains(run.Error, "interrupted") {
				t.Fatalf("dead run error = %q, want an interrupted reason", run.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dead run status = %q, want cancelled via the daemon's orphan finalizer", run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWingd_CancelOthersEvictionNamesKeyAndSuperseder(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 4)
	backends, _, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	victim := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-evict-victim",
			RunID:     "wingd-victim",
			Admission: testWingdAdmission(home, nil),
		})
		victim <- res
	}()
	gate.awaitStarted(t, "wingd-victim")

	aggressor, err := Run(context.Background(), backends, Options{
		Pipeline:  "wingd-e2e-evict-aggressor",
		RunID:     "wingd-aggressor",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil {
		t.Fatalf("aggressor run: %v", err)
	}
	if aggressor.Status != "success" {
		t.Fatalf("aggressor status = %q (err=%v), want success", aggressor.Status, aggressor.Error)
	}

	select {
	case res := <-victim:
		if res.Status != "cancelled" {
			t.Fatalf("victim status = %q (err=%v), want cancelled", res.Status, res.Error)
		}
		msg := ""
		if res.Error != nil {
			msg = res.Error.Error()
		}
		if !strings.Contains(msg, "wingd-e2e-deploy") || !strings.Contains(msg, "wingd-aggressor") {
			t.Fatalf("victim error = %q, want the contested key and the superseding run named", msg)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("victim never finished after eviction")
	}
}

func TestWingd_NodeGroupSerializesAcrossRuns(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, _, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	results := make(chan *Result, 2)
	launch := func(runID string) {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-node-sem",
			RunID:     runID,
			Admission: testWingdAdmission(home, nil),
		})
		results <- res
	}
	go launch("wingd-sem-a")
	go launch("wingd-sem-b")

	var first string
	select {
	case first = <-gate.started:
	case <-time.After(wingdTestWait):
		t.Fatal("neither node-semaphore run started")
	}
	second := "wingd-sem-a"
	if first == second {
		second = "wingd-sem-b"
	}
	awaitWaiter(t, home, nodeSemaphoreRunID(second, "locked"))

	close(gate.release)
	for range 2 {
		select {
		case res := <-results:
			if res == nil || res.Status != "success" {
				t.Fatalf("run result = %+v, want success", res)
			}
		case <-time.After(wingdTestWait):
			t.Fatal("node-semaphore run did not finish")
		}
	}
	if got := gate.peak.Load(); got != 1 {
		t.Fatalf("peak concurrent locked nodes = %d, want the daemon semaphore to serialize them", got)
	}
}

func TestWingd_NodeHostAdmissionAndNodeSemaphoreUseDistinctParticipants(t *testing.T) {
	cases := []struct {
		name          string
		runID         string
		hostNodeID    string
		semNodeID     string
		semName       string
		collisionNote string
	}{
		{
			name:          "same-node",
			runID:         "run-same-node",
			hostNodeID:    "shard-1",
			semNodeID:     "shard-1",
			semName:       "node-shard-lock",
			collisionNote: "same node host and semaphore participants",
		},
		{
			name:          "namespace-like-node",
			runID:         "run-namespace-node",
			hostNodeID:    "node-semaphore/shard-1",
			semNodeID:     "shard-1",
			semName:       "node-namespace-lock",
			collisionNote: "node id and semaphore namespace participants",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := wingdTestHome(t)
			startWingd(t, home, 8)
			backends, _, _ := openWingdBackends(t, home)
			la := testWingdAdmission(home, nil)

			ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
			defer cancel()

			hostLease, err := la.admitNode(ctx, backends, "runner-mode", tc.runID, tc.hostNodeID, nil)
			if err != nil {
				t.Fatalf("admit node host resources: %v", err)
			}
			defer hostLease.release()

			claim := wingwire.SemaphoreClaim{
				Name:     tc.semName,
				Capacity: 1,
				Cost:     1,
				Policy:   wingwire.PolicyQueue,
			}
			semLease, err := la.acquireNodeSlot(ctx, tc.runID, tc.semNodeID, claim, nil)
			if err != nil {
				t.Fatalf("acquire node semaphore after host admission: %v", err)
			}
			defer func() { _ = semLease.Release() }()

			if hostLease.leases[0].RunID == semLease.RunID {
				t.Fatalf("%s collided at %q", tc.collisionNote, semLease.RunID)
			}
		})
	}
}

func TestWingd_NodeGroupCancelOthersEvictsAcrossRuns(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	victim := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-node-evict-victim",
			RunID:     "wingd-node-victim",
			Admission: testWingdAdmission(home, nil),
		})
		victim <- res
	}()
	gate.awaitStarted(t, "wingd-node-victim")

	aggressor, err := Run(context.Background(), backends, Options{
		Pipeline:  "wingd-e2e-node-evict-aggressor",
		RunID:     "wingd-node-aggressor",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil {
		t.Fatalf("aggressor run: %v", err)
	}
	if aggressor.Status != "success" {
		t.Fatalf("aggressor status = %q (err=%v), want success (newest wins)", aggressor.Status, aggressor.Error)
	}

	select {
	case res := <-victim:
		if res.Status != "cancelled" {
			t.Fatalf("victim run status = %q (err=%v), want cancelled (preempted, not a job fault)", res.Status, res.Error)
		}
		node, nerr := st.GetNode(context.Background(), "wingd-node-victim", "locked")
		if nerr != nil {
			t.Fatalf("get victim node: %v", nerr)
		}
		if node.Error == "" || !strings.Contains(node.Error, "wingd-e2e-node-evict") || !strings.Contains(node.Error, "wingd-node-aggressor") {
			t.Fatalf("victim node error = %q, want the contested key and superseding run named", node.Error)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("victim never finished after node-level eviction")
	}
}

func TestWingd_DaemonFirstCancelRecoversStalledHolderWithoutDashboard(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingdCfg(t, wingd.Config{
		Home:    home,
		Version: "test",
		Sampler: stubSampler{wingd.HostStat{
			TotalCores: 2, TotalMemoryBytes: 64 << 30, FreeMemoryBytes: 64 << 30,
		}},
		HeadroomFraction: -1,
		GraceWindow:      -1,
		ProcSampler:      idleProcSampler{},
		StallInterval:    20 * time.Millisecond,
		StallWindow:      40 * time.Millisecond,
	})
	backends, st, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	ctx := context.Background()

	holder := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "stall-holder",
			Admission: testWingdAdmission(home, nil),
		})
		holder <- res
	}()
	gate.awaitStarted(t, "stall-holder")

	waiter := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "stall-waiter",
			Admission: testWingdAdmission(home, nil),
		})
		waiter <- res
	}()
	awaitWaiter(t, home, "stall-waiter")

	var recovery string
	deadline := time.Now().Add(wingdTestWait)
	for time.Now().Before(deadline) {
		h := findWingdHolder(t, home, "stall-holder")
		if h.Stalled && h.Recovery != "" {
			recovery = h.Recovery
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if recovery == "" {
		t.Fatal("stalled holder was never flagged with a recovery command")
	}
	if recovery != "sparkwing runs cancel --run stall-holder" {
		t.Fatalf("recovery command = %q, want the daemon-first cancel", recovery)
	}

	found, err := wingdclient.Cancel(ctx, wingdclient.Options{Home: home, Version: "test"}, "stall-holder")
	if err != nil {
		t.Fatalf("daemon-first cancel: %v", err)
	}
	if !found {
		t.Fatal("daemon did not know the local run; recovery would dead-end at a dashboard")
	}

	select {
	case res := <-holder:
		if res.Status != "cancelled" {
			t.Fatalf("holder status = %q (err=%v), want cancelled after daemon-first cancel", res.Status, res.Error)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("stalled holder never wound down after cancel")
	}

	gate.awaitStarted(t, "stall-waiter")
	close(gate.release)
	select {
	case res := <-waiter:
		if res.Status != "success" {
			t.Fatalf("promoted waiter status = %q, want success", res.Status)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("waiter never promoted after the holder released")
	}

	run, err := st.GetRun(ctx, "stall-holder")
	if err != nil {
		t.Fatalf("get holder run: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("stored holder status = %q, want cancelled", run.Status)
	}
}

func TestWingd_DaemonFirstCancelRemovesQueuedWaiterWithoutDashboard(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	backends, st, _ := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)
	ctx := context.Background()

	holder := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "cancel-holder",
			Admission: testWingdAdmission(home, nil),
		})
		holder <- res
	}()
	gate.awaitStarted(t, "cancel-holder")

	waiterA := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "cancel-waiter-a",
			Admission: testWingdAdmission(home, nil),
		})
		waiterA <- res
	}()
	awaitWaiter(t, home, "cancel-waiter-a")

	waiterB := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "cancel-waiter-b",
			Admission: testWingdAdmission(home, nil),
		})
		waiterB <- res
	}()
	awaitWaiter(t, home, "cancel-waiter-b")

	if w, ok := findQueuedWaiter(queryWingd(t, home), "cancel-waiter-b"); !ok || w.Position != 2 {
		t.Fatalf("waiter-b position before cancel = %d (present=%v), want 2", w.Position, ok)
	}

	found, err := wingdclient.Cancel(ctx, wingdclient.Options{Home: home, Version: "test"}, "cancel-waiter-a")
	if err != nil {
		t.Fatalf("daemon-first cancel: %v", err)
	}
	if !found {
		t.Fatal("daemon did not know the queued run; cancel would dead-end at a dashboard")
	}

	select {
	case res := <-waiterA:
		if res.Status != "cancelled" {
			t.Fatalf("cancelled waiter status = %q (err=%v), want cancelled", res.Status, res.Error)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("queued waiter never wound down after cancel")
	}

	deadline := time.Now().Add(wingdTestWait)
	for time.Now().Before(deadline) {
		qs := queryWingd(t, home)
		if _, stillQueued := findQueuedWaiter(qs, "cancel-waiter-a"); stillQueued {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		b, ok := findQueuedWaiter(qs, "cancel-waiter-b")
		if ok && b.Position == 1 && len(qs.Holders) == 1 && qs.Holders[0].RunID == "cancel-holder" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	qs := queryWingd(t, home)
	if _, stillQueued := findQueuedWaiter(qs, "cancel-waiter-a"); stillQueued {
		t.Fatal("cancelled waiter still in the queue after cancel")
	}
	b, ok := findQueuedWaiter(qs, "cancel-waiter-b")
	if !ok || b.Position != 1 {
		t.Fatalf("waiter-b position after cancel = %d (present=%v), want 1 (positions re-stated)", b.Position, ok)
	}
	if len(qs.Holders) != 1 || qs.Holders[0].RunID != "cancel-holder" {
		t.Fatalf("holder disturbed by waiter cancel: holders = %+v", qs.Holders)
	}

	run, err := st.GetRun(ctx, "cancel-waiter-a")
	if err != nil {
		t.Fatalf("get cancelled waiter run: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("stored waiter status = %q, want cancelled", run.Status)
	}

	close(gate.release)
	select {
	case res := <-holder:
		if res.Status != "success" {
			t.Fatalf("holder status = %q, want success (undisturbed)", res.Status)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("undisturbed holder never finished")
	}
	select {
	case res := <-waiterB:
		if res.Status != "success" {
			t.Fatalf("promoted waiter-b status = %q, want success", res.Status)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("promoted waiter-b never finished after holder released")
	}
}

func TestRunLocal_SIGINTFinalizesRunAsCancelledAndReleasesLease(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 2)
	_, st, p := openWingdBackends(t, home)
	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	done := make(chan *Result, 1)
	go func() {
		res, _ := RunLocal(context.Background(), p, Options{
			Pipeline:  "wingd-e2e-hold",
			RunID:     "wingd-sigint",
			State:     st,
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()
	gate.awaitStarted(t, "wingd-sigint")

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
	select {
	case res := <-done:
		if res == nil || res.Status != "cancelled" {
			t.Fatalf("result = %+v, want cancelled on SIGINT", res)
		}
		if res.Error == nil || !strings.Contains(res.Error.Error(), "interrupted by SIGINT") {
			t.Fatalf("error = %v, want the interrupting signal named as SIGINT", res.Error)
		}
	case <-time.After(wingdTestWait):
		t.Fatal("run did not finish after SIGINT")
	}

	run, err := st.GetRun(context.Background(), "wingd-sigint")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != "cancelled" || !strings.Contains(run.Error, "interrupted by SIGINT") {
		t.Fatalf("stored run = %q / %q, want cancelled with the signal named as SIGINT", run.Status, run.Error)
	}

	deadline := time.Now().Add(wingdTestWait)
	for {
		if len(queryWingd(t, home).Holders) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon still reports holders after the run finished: %+v", queryWingd(t, home).Holders)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
