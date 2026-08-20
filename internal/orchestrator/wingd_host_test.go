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

// registerHostTestPipelines registers one pipeline per shape the split
// semantics turn on, plus gated variants the concurrency regressions use
// to observe how many run at once.
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

		// Gated: the body reports and blocks, so overlapping work is
		// observable as a peak above one.
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
		// Two independent nodes in one run, both in a run-scoped
		// capacity-1 group: they would run in parallel if nothing held it.
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

// TestPlanPinsHostResources pins the line between a reservation only the
// daemon can hold and everything else. Concurrency groups are on the
// "everything else" side deliberately: without an admission they are
// honored by the shared store, which
// TestRun_DegradedConcurrencyGroupsStillSerialize proves empirically.
func TestPlanPinsHostResources(t *testing.T) {
	cases := []struct {
		pipeline  string
		pinned    bool
		wantWhere string
	}{
		{"host-implicit", false, ""},
		{"host-plan-resources", true, "a plan-level .Resources() pin"},
		{"host-node-resources", true, `node-level .Resources() pin on "heavy"`},
		{"host-plan-concurrency", false, ""},
		{"host-node-concurrency", false, ""},
		{"host-global-concurrency", false, ""},
	}
	for _, tc := range cases {
		pinned, where := planPinsHostResources(buildHostTestPlan(t, tc.pipeline))
		if pinned != tc.pinned {
			t.Errorf("%s: pinned = %v, want %v (where=%q)", tc.pipeline, pinned, tc.pinned, where)
			continue
		}
		if tc.wantWhere != "" && !strings.Contains(where, tc.wantWhere) {
			t.Errorf("%s: where = %q, want it to name %q", tc.pipeline, where, tc.wantWhere)
		}
	}
	if pinned, _ := planPinsHostResources(nil); pinned {
		t.Error("a nil plan pins nothing")
	}
}

// TestUnhostedOutcome_OnlyTheTwoUnusableBoxSentinels keeps the split
// pointed at the two conditions it exists for. An unreachable daemon, an
// unusable host binary, a policy rejection, or an exhausted takeover must
// keep failing loudly for everyone.
func TestUnhostedOutcome_OnlyTheTwoUnusableBoxSentinels(t *testing.T) {
	implicit := buildHostTestPlan(t, "host-implicit")
	other := []error{
		fmt.Errorf("x: %w", wingdclient.ErrDaemonUnreachable),
		fmt.Errorf("x: %w", wingdclient.ErrTakeoverExhausted),
		fmt.Errorf("x: %w", wingdclient.ErrProtocolTooOld),
		fmt.Errorf("x: %w", wingdclient.ErrDaemonHostUnusable),
		errors.New(`wingd: fail on "key"`),
		nil,
	}
	for _, err := range other {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		degrade, refusal := la.unhostedOutcome(err, implicit, false)
		if degrade || refusal != nil {
			t.Errorf("unhostedOutcome(%v) = (%v, %v), want the caller's own error kept", err, degrade, refusal)
		}
	}

	installed := &LocalAdmission{}
	if degrade, refusal := installed.unhostedOutcome(fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost), implicit, false); degrade || refusal != nil {
		t.Errorf("an installed-binary admission degraded: (%v, %v)", degrade, refusal)
	}
	var nilLA *LocalAdmission
	if degrade, refusal := nilLA.unhostedOutcome(wingdclient.ErrNoDaemonHost, implicit, false); degrade || refusal != nil {
		t.Errorf("nil admission returned (%v, %v)", degrade, refusal)
	}
}

// Only a host-resource pin fails closed on an empty box. A concurrency
// group does not, because it keeps working without the daemon.
func TestUnhostedOutcome_OnlyAHostPinFailsClosedOnAnEmptyBox(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	noHost := fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost)

	for _, pipeline := range []string{"host-plan-resources", "host-node-resources"} {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		degrade, refusal := la.unhostedOutcome(noHost, buildHostTestPlan(t, pipeline), false)
		if degrade || refusal == nil {
			t.Fatalf("%s: pinned run was not refused: (%v, %v)", pipeline, degrade, refusal)
		}
		msg := refusal.Error()
		for _, want := range []string{".Resources()", "install or update the sparkwing CLI", wingdclient.HostBinEnv, AllowUnadmittedEnv + "=1"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: refusal %q omits %q", pipeline, msg, want)
			}
		}
	}

	for _, pipeline := range []string{"host-plan-concurrency", "host-node-concurrency", "host-global-concurrency", "host-implicit"} {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		degrade, refusal := la.unhostedOutcome(noHost, buildHostTestPlan(t, pipeline), false)
		if !degrade || refusal != nil {
			t.Fatalf("%s: run without a host pin was refused: (%v, %v)", pipeline, degrade, refusal)
		}
	}
}

// A live daemon this client cannot speak to refuses every run, pinned or
// not: it is arbitrating the box, and joining it uncoordinated
// oversubscribes the machine rather than merely losing coordination.
func TestUnhostedOutcome_ALiveTooOldDaemonRefusesEveryRun(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	tooOld := fmt.Errorf("local admission: %w: daemon speaks protocol 2 (sparkwing v0.23.0), "+
		"this pipeline binary speaks protocol 3 (sparkwing v0.27.0). Install sparkwing v0.27.0 or newer",
		wingdclient.ErrDaemonTooOld)

	for _, pipeline := range []string{"host-implicit", "host-plan-concurrency", "host-plan-resources"} {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		degrade, refusal := la.unhostedOutcome(tooOld, buildHostTestPlan(t, pipeline), false)
		if degrade || refusal == nil {
			t.Fatalf("%s: a live too-old daemon did not refuse: (%v, %v)", pipeline, degrade, refusal)
		}
		msg := refusal.Error()
		for _, want := range []string{"v0.23.0", "v0.27.0", "arbitrating this box", AllowUnadmittedEnv} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: refusal %q omits %q", pipeline, msg, want)
			}
		}
		if !errors.Is(refusal, wingdclient.ErrDaemonTooOld) {
			t.Errorf("%s: refusal dropped the ErrDaemonTooOld sentinel", pipeline)
		}
	}
}

// A run with nothing pinned degrades, and says so exactly once however
// many admission calls meet the same box. The warning has to be honest
// about what is and is not still enforced.
func TestUnhostedOutcome_DegradeWarnsOnceAndNamesWhatIsLost(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	plan := buildHostTestPlan(t, "host-implicit")
	var lines []string
	la := &LocalAdmission{
		PipelineClient: true,
		Logf:           func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	for range 3 {
		degrade, refusal := la.unhostedOutcome(fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost), plan, false)
		if !degrade || refusal != nil {
			t.Fatalf("implicit run did not degrade: (%v, %v)", degrade, refusal)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("degrade announced %d times, want exactly 1: %v", len(lines), lines)
	}
	for _, want := range []string{"CPU and memory are not arbitrated", "still enforced", "sparkwing doctor"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("warning %q omits %q", lines[0], want)
		}
	}
}

// The escape hatch is strict: only "1". A value like "off" or "false"
// reads as an attempt to keep the gate on, and a lenient parser that
// treated any non-empty value as authorization would disable it.
func TestAllowUnadmitted_OnlyExactlyOne(t *testing.T) {
	plan := buildHostTestPlan(t, "host-plan-resources")
	noHost := fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost)

	t.Setenv(AllowUnadmittedEnv, "1")
	la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
	if degrade, refusal := la.unhostedOutcome(noHost, plan, false); !degrade || refusal != nil {
		t.Fatalf(`%s=1 did not degrade a pinned run: (%v, %v)`, AllowUnadmittedEnv, degrade, refusal)
	}

	for _, value := range []string{"", "0", "off", "no", "false", "true", "yes", "TRUE", " 1", "1 ", "2"} {
		t.Setenv(AllowUnadmittedEnv, value)
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		if degrade, _ := la.unhostedOutcome(noHost, plan, false); degrade {
			t.Errorf("%s=%q was read as authorization; only \"1\" may disable the gate", AllowUnadmittedEnv, value)
		}
	}
}

// A dry run mutates nothing and finishes in seconds, so it is exempt from
// both refusals -- and it is the command an operator uses to find out
// what a box would do, which refusing would defeat.
func TestUnhostedOutcome_DryRunIsExempt(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	pinned := buildHostTestPlan(t, "host-plan-resources")
	for _, err := range []error{
		fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost),
		fmt.Errorf("x: %w", wingdclient.ErrDaemonTooOld),
	} {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		degrade, refusal := la.unhostedOutcome(err, pinned, true)
		if !degrade || refusal != nil {
			t.Fatalf("dry run of a pinned pipeline was refused on %v: (%v, %v)", err, degrade, refusal)
		}
	}
}

// TestPipelineAdmission_NeverHosts pins the wiring: a pipeline binary's
// admission shares the daemon rather than replacing it, spawns something
// other than itself when one is needed, and threads the parent lease and
// origin through unchanged.
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

// PipelineClient and a non-self-exec Spawn are one stance. Setting the
// first without the second must not fall through to the default that
// re-execs this binary as the daemon.
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

// runHostTest drives a real Run through a pipeline-binary admission
// pointed at a home no daemon serves, with nothing available to host one.
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

// End to end: a pinned run on an empty box fails, an unpinned one
// succeeds uncoordinated.
func TestRun_EmptyBoxSplitsOnTheHostPin(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")

	res, _ := runHostTest(t, "host-plan-resources")
	if res.Status != "failed" {
		t.Fatalf("pinned run status = %q, want failed", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), AllowUnadmittedEnv) {
		t.Fatalf("pinned run error = %v, want the actionable refusal", res.Error)
	}

	res, warnings := runHostTest(t, "host-implicit")
	if res.Status != "success" {
		t.Fatalf("implicit run status = %q (%v), want success", res.Status, res.Error)
	}
	if got := strings.Count(warnings, "running without local coordination"); got != 1 {
		t.Fatalf("implicit run warned %d times, want 1: %q", got, warnings)
	}
}

func TestRun_EscapeHatchLetsAPinnedRunProceedUnadmitted(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "1")
	res, warnings := runHostTest(t, "host-plan-resources")
	if res.Status != "success" {
		t.Fatalf("pinned run under the escape hatch = %q (%v), want success", res.Status, res.Error)
	}
	if !strings.Contains(warnings, "running without local coordination") {
		t.Fatalf("escape-hatch run said nothing about being uncoordinated: %q", warnings)
	}
}

// TestRun_DegradedConcurrencyGroupsStillSerialize is the evidence behind
// narrowing the refusal to host pins. Two degraded runs of a capacity-1
// box-scoped group must not overlap: with no admission the whole run
// takes the no-daemon path, where the shared store enforces the group.
// If that ever stops being true, refusing these runs becomes the right
// answer again -- and this is the test that would say so.
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
			backends, _, _ := openWingdBackends(t, home)

			gate := newWingdGate()
			hostTestGate.Store(gate)
			t.Cleanup(func() { hostTestGate.Store(nil) })

			var warnings strings.Builder
			results := make(chan *Result, 2)
			for i, runID := range []string{"degraded-a", "degraded-b"} {
				go func(runID string) {
					res, err := Run(context.Background(), backends, Options{
						Pipeline:  tc.pipeline,
						RunID:     runID,
						Admission: unhostedAdmission(home, &warnings),
					})
					if err != nil {
						t.Errorf("run %s: %v", runID, err)
					}
					results <- res
				}(runID)
				if i == 0 {
					// safety: let the first run take the slot before the
					// second asks, so a failure means the group did not hold
					// rather than that the race went the other way.
					gate.awaitStarted(t, "degraded-a")
				}
			}
			// The second must not start while the first holds the slot.
			select {
			case started := <-gate.started:
				t.Fatalf("%q started while %q held a capacity-1 box group", started, "degraded-a")
			case <-time.After(750 * time.Millisecond):
			}
			close(gate.release)

			for range 2 {
				select {
				case res := <-results:
					if res.Status != "success" {
						t.Fatalf("degraded run %s = %q (%v)", res.RunID, res.Status, res.Error)
					}
				case <-time.After(wingdTestWait):
					t.Fatal("degraded runs did not finish")
				}
			}
			if peak := gate.peak.Load(); peak != 1 {
				t.Fatalf("peak concurrency = %d, want 1 -- the shared store did not enforce the box group", peak)
			}
			if !strings.Contains(warnings.String(), "still enforced") {
				t.Errorf("degrade warning did not tell the operator groups are still enforced: %q", warnings.String())
			}
		})
	}
}

// Run scope is keyed by run id, so the store enforces it completely: two
// independent nodes of one degraded run sharing a run-scoped capacity-1
// group must still serialize, even though nothing stops them being
// dispatched at the same time.
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
	waitForRunScopePopulation(t, ctx, st, scopedGroupKey(runGroup, "degraded-run-scope"))
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

func waitForRunScopePopulation(t *testing.T, ctx context.Context, st *store.Store, key string) {
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
			t.Fatalf("read degraded run-scope concurrency state: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("degraded run-scope population did not reach one holder and one waiter")
		case <-poll.C:
		}
	}
}
