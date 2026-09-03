package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	boxGroup    = sparkwing.NewConcurrencyGroup("host-test-box", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeBox})
	runGroup    = sparkwing.NewConcurrencyGroup("host-test-run", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeRun})
	globalGroup = sparkwing.NewConcurrencyGroup("host-test-global", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeGlobal})
)

var (
	hostTestPipelinesOnce sync.Once
	hostTestGate          atomic.Pointer[wingdGate]
)

func registerHostTestPipelines() {
	hostTestPipelinesOnce.Do(func() {
		register := func(name string, build func(*sparkwing.Plan, sparkwing.RunContext)) {
			sparkwing.Register[sparkwing.NoInputs](name,
				func() sparkwing.Pipeline[sparkwing.NoInputs] { return &hostTestPipe{build: build} })
		}
		register("host-implicit", func(*sparkwing.Plan, sparkwing.RunContext) {})
		register("host-plan-resources", func(plan *sparkwing.Plan, _ sparkwing.RunContext) {
			plan.Resources(sparkwing.Cores(2))
		})
		register("host-node-resources", func(plan *sparkwing.Plan, _ sparkwing.RunContext) {
			sparkwing.Job(plan, "heavy", func(context.Context) error { return nil }).Resources(sparkwing.Cores(2))
		})
		register("host-plan-concurrency", func(plan *sparkwing.Plan, _ sparkwing.RunContext) {
			plan.Concurrency(boxGroup)
		})
		register("host-node-concurrency", func(plan *sparkwing.Plan, _ sparkwing.RunContext) {
			sparkwing.Job(plan, "gated", func(context.Context) error { return nil }).Concurrency(boxGroup)
		})
		register("host-global-concurrency", func(plan *sparkwing.Plan, _ sparkwing.RunContext) {
			plan.Concurrency(globalGroup)
		})

		gated := func(plan *sparkwing.Plan, id, runID string) *sparkwing.JobNode {
			return sparkwing.Job(plan, id, func(ctx context.Context) error {
				if g := hostTestGate.Load(); g != nil {
					return g.run(ctx, runID)
				}
				return nil
			})
		}
		register("host-gated-plan-box", func(plan *sparkwing.Plan, rc sparkwing.RunContext) {
			plan.Concurrency(boxGroup)
			gated(plan, "work", rc.RunID)
		})
		register("host-gated-node-box", func(plan *sparkwing.Plan, rc sparkwing.RunContext) {
			gated(plan, "work", rc.RunID).Concurrency(boxGroup)
		})

		register("host-gated-run-nodes", func(plan *sparkwing.Plan, rc sparkwing.RunContext) {
			gated(plan, "work-a", rc.RunID).Concurrency(runGroup)
			gated(plan, "work-b", rc.RunID).Concurrency(runGroup)
		})
	})
}

type hostTestPipe struct {
	sparkwing.Base
	build func(*sparkwing.Plan, sparkwing.RunContext)
}

func (p *hostTestPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "step", func(context.Context) error { return nil })
	p.build(plan, rc)
	return nil
}

func buildHostTestPlan(t *testing.T, name string) *sparkwing.Plan {
	t.Helper()
	registerHostTestPipelines()
	reg, ok := sparkwing.Lookup(name)
	if !ok {
		t.Fatalf("pipeline %q is not registered", name)
	}
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: name, RunID: "host-test"})
	if err != nil {
		t.Fatalf("build plan %q: %v", name, err)
	}
	return plan
}

func TestUnhostedOutcome_DegradesOnEveryStandaloneSentinel(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	degrading := []error{
		fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost),
		fmt.Errorf("x: %w", wingdclient.ErrDaemonHostUnusable),
		fmt.Errorf("x: %w", wingdclient.ErrDaemonHostFailed),
		fmt.Errorf("x: %w", wingdclient.ErrDaemonTooOld),
		fmt.Errorf("x: %w", wingdclient.ErrDaemonLacksOperation),
		fmt.Errorf("x: %w", wingdclient.ErrProtocolTooOld),
	}
	for _, err := range degrading {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		if !la.unhostedOutcome(err) {
			t.Errorf("unhostedOutcome(%v) refused a run the degradation rule covers", err)
		}
	}
}

func TestUnhostedOutcome_KeepsEveryOtherFailure(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	other := []error{
		fmt.Errorf("x: %w", wingdclient.ErrDaemonUnreachable),
		fmt.Errorf("x: %w", wingdclient.ErrTakeoverExhausted),
		fmt.Errorf("x: %w", wingdclient.ErrBuildMismatch),
		errors.New(`wingd: fail on "key"`),
		nil,
	}
	for _, err := range other {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		if la.unhostedOutcome(err) {
			t.Errorf("unhostedOutcome(%v) degraded a failure that is not a version gap", err)
		}
	}

	installed := &LocalAdmission{}
	if installed.unhostedOutcome(fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost)) {
		t.Error("an installed-binary admission degraded")
	}
	var nilLA *LocalAdmission
	if nilLA.unhostedOutcome(wingdclient.ErrNoDaemonHost) {
		t.Error("nil admission degraded")
	}
}

func TestUnhostedOutcome_APinnedRunDegradesLikeAnyOther(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	noHost := fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost)
	for _, pipeline := range []string{"host-plan-resources", "host-node-resources", "host-implicit"} {
		buildHostTestPlan(t, pipeline)
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		if !la.unhostedOutcome(noHost) {
			t.Errorf("%s: a .Resources() pin refused a run on an empty box", pipeline)
		}
	}
}

func TestUnhostedOutcome_SaysNothingItself(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	var lines []string
	la := &LocalAdmission{
		PipelineClient: true,
		Logf:           func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	for range 3 {
		if !la.unhostedOutcome(fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost)) {
			t.Fatal("implicit run did not degrade")
		}
	}
	if len(lines) != 0 {
		t.Fatalf("degrade printed %d line(s) beside the run's own standalone block: %v", len(lines), lines)
	}
}

func TestAllowUnadmitted_OnlyExactlyOne(t *testing.T) {
	for _, value := range []string{"", "0", "off", "no", "false", "true", "yes", "TRUE", " 1", "1 ", "2"} {
		t.Setenv(AllowUnadmittedEnv, value)
		if allowUnadmitted() {
			t.Errorf("%s=%q was read as authorization; only exactly 1 turns the check off", AllowUnadmittedEnv, value)
		}
	}
	t.Setenv(AllowUnadmittedEnv, "1")
	if !allowUnadmitted() {
		t.Errorf("%s=1 did not turn the check off", AllowUnadmittedEnv)
	}
}

func TestPipelineAdmission_NeverHosts(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "/opt/sparkwing/bin/sparkwing")
	la := pipelineAdmission("parent-token", wingwire.OriginLocal)
	if la == nil {
		t.Fatal("pipelineAdmission returned nil")
	}
	if !la.PipelineClient {
		t.Error("pipeline-binary admission must be marked as a non-hosting client")
	}
	if !la.clientOptions().NoTakeover {
		t.Error("pipeline-binary admission must never take the daemon over")
	}
	if la.Spawn == nil {
		t.Error("pipeline-binary admission must carry an explicit spawn, never the self-exec default")
	}
	if la.ParentLeaseToken != "parent-token" {
		t.Errorf("parent lease token %q not threaded through", la.ParentLeaseToken)
	}
	if la.Origin != wingwire.OriginLocal {
		t.Errorf("origin %q not threaded through", la.Origin)
	}
}

func TestPipelineAdmission_NoHostResolvableSpawnsNothing(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "")
	t.Setenv("PATH", t.TempDir())
	la := pipelineAdmission("", wingwire.OriginLocal)
	if la == nil {
		t.Fatal("pipelineAdmission returned nil; the plan decides whether a run may proceed, not this")
	}
	if err := la.Spawn("", ""); !errors.Is(err, wingdclient.ErrNoDaemonHost) {
		t.Fatalf("spawn on a machine with no sparkwing returned %v, want ErrNoDaemonHost", err)
	}
}

func TestClientOptions_PipelineClientNeverGetsTheSelfExecDefault(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "")
	t.Setenv("PATH", t.TempDir())
	la := &LocalAdmission{PipelineClient: true}
	opts := la.clientOptions()
	if opts.Spawn == nil {
		t.Fatal("a PipelineClient admission was left with the self-exec default spawn")
	}
	if err := opts.Spawn("", ""); !errors.Is(err, wingdclient.ErrNoDaemonHost) {
		t.Fatalf("resolved spawn returned %v, want ErrNoDaemonHost on a machine with no sparkwing", err)
	}
}

func runHostTest(t *testing.T, pipeline string) (*Result, string) {
	t.Helper()
	registerHostTestPipelines()
	home := wingdTestHome(t)
	backends, _, _ := openWingdBackends(t, home)

	var warnings strings.Builder
	res, err := Run(context.Background(), backends, Options{
		Pipeline:  pipeline,
		RunID:     "host-" + pipeline + "-" + t.Name(),
		Admission: unhostedAdmission(home, &warnings),
	})
	if err != nil {
		t.Fatalf("Run returned a transport error: %v", err)
	}
	return res, warnings.String()
}

func unhostedAdmission(home string, warnings io.Writer) *LocalAdmission {
	return &LocalAdmission{
		Home:           home,
		Version:        "test",
		Out:            io.Discard,
		Spawn:          wingdclient.NoHostSpawn,
		PipelineClient: true,
		Logf:           func(f string, a ...any) { fmt.Fprintf(warnings, f+"\n", a...) },
		DialTimeout:    100 * time.Millisecond,
		Backoff:        10 * time.Millisecond,
	}
}

func TestRun_EmptyBoxRunsAPinnedPipelineAnyway(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	for _, pipeline := range []string{"host-plan-resources", "host-implicit"} {
		res, warnings := runHostTest(t, pipeline)
		if res.Status != "success" {
			t.Fatalf("%s status = %q (%v), want success", pipeline, res.Status, res.Error)
		}
		if warnings != "" {
			t.Fatalf("%s printed %q through admission; the run's own block is the only line", pipeline, warnings)
		}
	}
}

func TestRun_EscapeHatchLetsAPinnedRunProceedUnadmitted(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "1")
	res, _ := runHostTest(t, "host-plan-resources")
	if res.Status != "success" {
		t.Fatalf("pinned run under the escape hatch = %q (%v), want success", res.Status, res.Error)
	}
}

func TestRun_DegradedConcurrencyGroupsStillSerialize(t *testing.T) {
	cases := []struct{ name, pipeline string }{
		{"plan-level box scope", "host-gated-plan-box"},
		{"node-level box scope", "host-gated-node-box"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(AllowUnadmittedEnv, "")
			registerHostTestPipelines()
			home := wingdTestHome(t)
			backends, st, _ := openWingdBackends(t, home)

			gate := newWingdGate()
			hostTestGate.Store(gate)
			t.Cleanup(func() { hostTestGate.Store(nil) })

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			type runResult struct {
				result *Result
				err    error
			}
			results := make(chan runResult, 2)
			launched, joined := 0, 0
			t.Cleanup(func() {
				release()
				cancel()
				timer := time.NewTimer(time.Second)
				defer timer.Stop()
				for joined < launched {
					select {
					case <-results:
						joined++
					case <-timer.C:
						t.Error("degraded box-scope runs did not stop during cleanup")
						return
					}
				}
			})

			var warnings syncBuffer
			launch := func(runID string) {
				launched++
				go func(runID string) {
					res, err := Run(ctx, backends, Options{
						Pipeline:  tc.pipeline,
						RunID:     runID,
						Admission: unhostedAdmission(home, &warnings),
					})
					results <- runResult{result: res, err: err}
				}(runID)
			}
			launch("degraded-a")
			gate.awaitStarted(t, "degraded-a")
			launch("degraded-b")
			waitForDegradedConcurrencyPopulation(t, ctx, st, scopedGroupKey(boxGroup, ""))

			select {
			case started := <-gate.started:
				t.Fatalf("%q started while %q held a capacity-1 box group", started, "degraded-a")
			default:
			}
			release()

			for range 2 {
				select {
				case outcome := <-results:
					joined++
					if outcome.err != nil {
						t.Fatalf("degraded run failed: %v", outcome.err)
					}
					if outcome.result.Status != "success" {
						t.Fatalf("degraded run %s = %q (%v)", outcome.result.RunID, outcome.result.Status, outcome.result.Error)
					}
				case <-ctx.Done():
					t.Fatal("degraded runs did not finish")
				}
			}
			if peak := gate.peak.Load(); peak != 1 {
				t.Fatalf("peak concurrency = %d, want 1 -- the shared store did not enforce the box group", peak)
			}
		})
	}
}

func TestRun_DegradedRunScopeStillSerializes(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	registerHostTestPipelines()
	home := wingdTestHome(t)
	backends, st, _ := openWingdBackends(t, home)

	gate := newWingdGate()
	hostTestGate.Store(gate)
	t.Cleanup(func() { hostTestGate.Store(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	type runResult struct {
		result *Result
		err    error
	}
	done := make(chan runResult, 1)
	joined := false
	var warnings strings.Builder
	go func() {
		res, err := Run(ctx, backends, Options{
			Pipeline:    "host-gated-run-nodes",
			RunID:       "degraded-run-scope",
			MaxParallel: 4,
			Admission:   unhostedAdmission(home, &warnings),
		})
		done <- runResult{result: res, err: err}
	}()
	t.Cleanup(func() {
		release()
		cancel()
		if joined {
			return
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("degraded run-scope run did not stop during cleanup")
		}
	})

	gate.awaitStarted(t, "degraded-run-scope")
	waitForDegradedConcurrencyPopulation(t, ctx, st, scopedGroupKey(runGroup, "degraded-run-scope"))
	select {
	case started := <-gate.started:
		t.Fatalf("%q entered while the run-scoped holder was blocked", started)
	default:
	}
	release()

	var outcome runResult
	select {
	case outcome = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("degraded run-scope run did not finish after release")
	}
	cancel()
	if outcome.err != nil {
		t.Fatalf("Run: %v", outcome.err)
	}
	if outcome.result.Status != "success" {
		t.Fatalf("degraded run-scope run = %q (%v), want success", outcome.result.Status, outcome.result.Error)
	}
	if peak := gate.peak.Load(); peak != 1 {
		t.Fatalf("peak concurrency = %d, want 1 under a run-scoped capacity-1 group", peak)
	}
}

func waitForDegradedConcurrencyPopulation(t *testing.T, ctx context.Context, st *store.Store, key string) {
	t.Helper()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		state, err := st.GetConcurrencyState(ctx, key)
		switch {
		case err == nil && len(state.Holders) == 1 && len(state.Waiters) == 1:
			return
		case err == nil:
		case errors.Is(err, store.ErrNotFound):
		default:
			t.Fatalf("read degraded concurrency state: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("degraded concurrency population did not reach one holder and one waiter")
		case <-poll.C:
		}
	}
}
