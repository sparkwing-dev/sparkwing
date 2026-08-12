package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	boxGroup    = sparkwing.NewConcurrencyGroup("host-test-box", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeBox})
	globalGroup = sparkwing.NewConcurrencyGroup("host-test-global", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeGlobal})
)

var hostTestPipelinesOnce sync.Once

// registerHostTestPipelines registers one pipeline per shape of
// declaration the split semantics turn on.
func registerHostTestPipelines() {
	hostTestPipelinesOnce.Do(func() {
		register := func(name string, build func(*sparkwing.Plan)) {
			sparkwing.Register[sparkwing.NoInputs](name,
				func() sparkwing.Pipeline[sparkwing.NoInputs] { return &hostTestPipe{build: build} })
		}
		register("host-implicit", func(plan *sparkwing.Plan) {})
		register("host-plan-resources", func(plan *sparkwing.Plan) {
			plan.Resources(sparkwing.Cores(2))
		})
		register("host-node-resources", func(plan *sparkwing.Plan) {
			sparkwing.Job(plan, "heavy", func(context.Context) error { return nil }).Resources(sparkwing.Cores(2))
		})
		register("host-plan-concurrency", func(plan *sparkwing.Plan) {
			plan.Concurrency(boxGroup)
		})
		register("host-node-concurrency", func(plan *sparkwing.Plan) {
			sparkwing.Job(plan, "gated", func(context.Context) error { return nil }).Concurrency(boxGroup)
		})
		register("host-global-concurrency", func(plan *sparkwing.Plan) {
			plan.Concurrency(globalGroup)
		})
	})
}

type hostTestPipe struct {
	sparkwing.Base
	build func(*sparkwing.Plan)
}

func (p *hostTestPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "step", func(context.Context) error { return nil })
	p.build(plan)
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

// TestPlanDeclaresLocalAdmission pins the line between an author asking
// the local daemon for something and the orchestrator charging a default
// on the author's behalf. Only the first may fail a run closed.
func TestPlanDeclaresLocalAdmission(t *testing.T) {
	cases := []struct {
		pipeline string
		declared bool
		wantWhy  string
	}{
		{"host-implicit", false, ""},
		{"host-plan-resources", true, "a plan-level .Resources() pin"},
		{"host-node-resources", true, "a node-level .Resources() pin"},
		{"host-plan-concurrency", true, `concurrency group "host-test-box"`},
		{"host-node-concurrency", true, `concurrency group "host-test-box"`},
		// A global-scope group pools across the fleet through the shared
		// store and never reaches the local daemon, so it is not a local
		// declaration and must not fail a run closed for the daemon's sake.
		{"host-global-concurrency", false, ""},
	}
	for _, tc := range cases {
		plan := buildHostTestPlan(t, tc.pipeline)
		declared, why := planDeclaresLocalAdmission(plan)
		if declared != tc.declared {
			t.Errorf("%s: declared = %v, want %v (why=%q)", tc.pipeline, declared, tc.declared, why)
			continue
		}
		if tc.wantWhy != "" && !strings.Contains(why, tc.wantWhy) {
			t.Errorf("%s: why = %q, want it to name %q", tc.pipeline, why, tc.wantWhy)
		}
	}
	if declared, _ := planDeclaresLocalAdmission(nil); declared {
		t.Error("a nil plan declares nothing")
	}
}

// TestUnhostedOutcome_OnlyTheTwoUnusableBoxSentinels keeps the split
// pointed at the two conditions it exists for. An unreachable daemon, a
// policy rejection, or an exhausted takeover must keep failing loudly for
// everyone: something is there, and running uncoordinated alongside it
// oversubscribes the box.
func TestUnhostedOutcome_OnlyTheTwoUnusableBoxSentinels(t *testing.T) {
	implicit := buildHostTestPlan(t, "host-implicit")
	other := []error{
		fmt.Errorf("x: %w", wingdclient.ErrDaemonUnreachable),
		fmt.Errorf("x: %w", wingdclient.ErrTakeoverExhausted),
		fmt.Errorf("x: %w", wingdclient.ErrProtocolTooOld),
		errors.New(`wingd: fail on "key"`),
		nil,
	}
	for _, err := range other {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		degrade, refusal := la.unhostedOutcome(err, implicit)
		if degrade || refusal != nil {
			t.Errorf("unhostedOutcome(%v) = (%v, %v), want the caller's own error kept", err, degrade, refusal)
		}
	}

	// An installed binary never degrades: it can host the daemon itself,
	// so "nothing can host one" is not a state it can be in honestly.
	installed := &LocalAdmission{}
	degrade, refusal := installed.unhostedOutcome(fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost), implicit)
	if degrade || refusal != nil {
		t.Errorf("an installed-binary admission degraded: (%v, %v)", degrade, refusal)
	}

	var nilLA *LocalAdmission
	if degrade, refusal := nilLA.unhostedOutcome(wingdclient.ErrNoDaemonHost, implicit); degrade || refusal != nil {
		t.Errorf("nil admission returned (%v, %v)", degrade, refusal)
	}
}

// A declaring run on a box with no daemon and nothing to host one must
// fail closed, naming what it declared, the fix, and the escape hatch.
func TestUnhostedOutcome_DeclaringRunFailsClosed(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	plan := buildHostTestPlan(t, "host-plan-concurrency")
	la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}

	degrade, refusal := la.unhostedOutcome(fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost), plan)
	if degrade {
		t.Fatal("a declaring run degraded to unadmitted")
	}
	if refusal == nil {
		t.Fatal("a declaring run on an unusable box was not refused")
	}
	msg := refusal.Error()
	for _, want := range []string{
		`concurrency group "host-test-box"`,
		"install or update the sparkwing CLI",
		wingdclient.HostBinEnv,
		AllowUnadmittedEnv + "=1",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q omits %q", msg, want)
		}
	}
}

// The too-old-daemon refusal must carry both versions and the minimum
// release, which it does by wrapping the client's own sentence rather than
// restating it.
func TestUnhostedOutcome_DeclaringRunFailsClosedOnATooOldDaemon(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	plan := buildHostTestPlan(t, "host-plan-resources")
	la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}

	tooOld := fmt.Errorf("local admission: %w: daemon speaks protocol 2 (sparkwing v0.23.0), "+
		"this pipeline binary speaks protocol 3 (sparkwing v0.27.0). Install sparkwing v0.24.0 or newer",
		wingdclient.ErrDaemonTooOld)
	degrade, refusal := la.unhostedOutcome(tooOld, plan)
	if degrade || refusal == nil {
		t.Fatalf("a declaring run met a too-old daemon and was not refused: (%v, %v)", degrade, refusal)
	}
	msg := refusal.Error()
	for _, want := range []string{"a plan-level .Resources() pin", "v0.23.0", "v0.27.0", "v0.24.0 or newer", AllowUnadmittedEnv} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q omits %q", msg, want)
		}
	}
}

// A run with only implicit default reservations degrades, and says so
// exactly once however many admission calls meet the same box.
func TestUnhostedOutcome_ImplicitRunDegradesWithOneWarning(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")
	plan := buildHostTestPlan(t, "host-implicit")
	var lines []string
	la := &LocalAdmission{
		PipelineClient: true,
		Logf:           func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	for range 3 {
		degrade, refusal := la.unhostedOutcome(fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost), plan)
		if !degrade || refusal != nil {
			t.Fatalf("implicit run did not degrade: (%v, %v)", degrade, refusal)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("degrade announced %d times, want exactly 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "running without local coordination") {
		t.Errorf("warning %q does not say the run is uncoordinated", lines[0])
	}
}

// The escape hatch flips the declaring case to the degrade, so an
// operator who knows their box runs one thing at a time is not blocked.
func TestUnhostedOutcome_EscapeHatchDegradesADeclaringRun(t *testing.T) {
	plan := buildHostTestPlan(t, "host-plan-concurrency")
	for _, value := range []string{"1", "true", "yes"} {
		t.Setenv(AllowUnadmittedEnv, value)
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		degrade, refusal := la.unhostedOutcome(fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost), plan)
		if !degrade || refusal != nil {
			t.Fatalf("%s=%q did not degrade a declaring run: (%v, %v)", AllowUnadmittedEnv, value, degrade, refusal)
		}
	}
	for _, value := range []string{"", "0", "false", "no"} {
		t.Setenv(AllowUnadmittedEnv, value)
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		if degrade, _ := la.unhostedOutcome(fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost), plan); degrade {
			t.Fatalf("%s=%q was read as authorization", AllowUnadmittedEnv, value)
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

// runHostTest drives a real Run through a pipeline-binary admission
// pointed at a home no daemon serves, with nothing available to host one.
func runHostTest(t *testing.T, pipeline string) (*Result, string) {
	t.Helper()
	registerHostTestPipelines()
	home := wingdTestHome(t)
	backends, _, _ := openWingdBackends(t, home)

	var warnings strings.Builder
	admission := &LocalAdmission{
		Home:           home,
		Version:        "test",
		Out:            io.Discard,
		Spawn:          wingdclient.NoHostSpawn,
		PipelineClient: true,
		Logf:           func(f string, a ...any) { fmt.Fprintf(&warnings, f+"\n", a...) },
		DialTimeout:    100 * time.Millisecond,
		Backoff:        10 * time.Millisecond,
	}
	res, err := Run(context.Background(), backends, Options{
		Pipeline:  pipeline,
		RunID:     "host-" + pipeline + "-" + t.Name(),
		Admission: admission,
	})
	if err != nil {
		t.Fatalf("Run returned a transport error: %v", err)
	}
	return res, warnings.String()
}

// End to end: a declaring run on an unusable box fails, and an
// implicit-only one succeeds uncoordinated. Both go through the real
// admission path, so this also proves the degrade leaves the rest of the
// run on the existing no-daemon path rather than re-meeting the daemon at
// every node.
func TestRun_UnusableBoxSplitsOnWhatThePipelineDeclared(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "")

	res, _ := runHostTest(t, "host-plan-concurrency")
	if res.Status != "failed" {
		t.Fatalf("declaring run status = %q, want failed", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), AllowUnadmittedEnv) {
		t.Fatalf("declaring run error = %v, want the actionable refusal", res.Error)
	}

	res, warnings := runHostTest(t, "host-implicit")
	if res.Status != "success" {
		t.Fatalf("implicit run status = %q (%v), want success", res.Status, res.Error)
	}
	if got := strings.Count(warnings, "running without local coordination"); got != 1 {
		t.Fatalf("implicit run warned %d times, want 1: %q", got, warnings)
	}
}

func TestRun_EscapeHatchLetsADeclaringRunProceedUnadmitted(t *testing.T) {
	t.Setenv(AllowUnadmittedEnv, "1")
	res, warnings := runHostTest(t, "host-plan-concurrency")
	if res.Status != "success" {
		t.Fatalf("declaring run under the escape hatch = %q (%v), want success", res.Status, res.Error)
	}
	if !strings.Contains(warnings, "running without local coordination") {
		t.Fatalf("escape-hatch run said nothing about being uncoordinated: %q", warnings)
	}
}
