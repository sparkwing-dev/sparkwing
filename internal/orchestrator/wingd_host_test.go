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
		if !la.unhostedOutcome(err, unhosted{}) {
			t.Errorf("unhostedOutcome(%v) refused a run the degradation rule covers", err)
		}
	}
}

func TestUnhostedOutcome_KeepsEveryOtherFailure(t *testing.T) {
	other := []error{
		fmt.Errorf("x: %w", wingdclient.ErrDaemonUnreachable),
		fmt.Errorf("x: %w", wingdclient.ErrTakeoverExhausted),
		fmt.Errorf("x: %w", wingdclient.ErrBuildMismatch),
		errors.New(`wingd: fail on "key"`),
		nil,
	}
	for _, err := range other {
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		if la.unhostedOutcome(err, unhosted{}) {
			t.Errorf("unhostedOutcome(%v) degraded a failure that is not a version gap", err)
		}
	}

	installed := &LocalAdmission{}
	if installed.unhostedOutcome(fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost), unhosted{}) {
		t.Error("an installed-binary admission degraded")
	}
	var nilLA *LocalAdmission
	if nilLA.unhostedOutcome(wingdclient.ErrNoDaemonHost, unhosted{}) {
		t.Error("nil admission degraded")
	}
}

func TestUnhostedOutcome_APinnedRunDegradesLikeAnyOther(t *testing.T) {
	noHost := fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost)
	for _, pipeline := range []string{"host-plan-resources", "host-node-resources", "host-implicit"} {
		buildHostTestPlan(t, pipeline)
		la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
		if !la.unhostedOutcome(noHost, unhosted{}) {
			t.Errorf("%s: a .Resources() pin refused a run on an empty box", pipeline)
		}
	}
}

func TestUnhostedOutcome_RefusesADaemonThatCannotReadItsOwnStore(t *testing.T) {
	refusal := fmt.Errorf("local admission: %w", &wingdclient.AdmissionError{
		Policy: wingwire.PolicyFail,
		Key:    terminalCheckKey,
		Reason: "daemon v0.38.2 could not read the runs store: this state database uses a-feature-from-the-future",
	})
	la := &LocalAdmission{PipelineClient: true, Logf: func(string, ...any) {}}
	if la.unhostedOutcome(refusal, unhosted{reason: standaloneDaemonOlder}) {
		t.Error("a corrupt store on a daemon serving no api.sock degraded; that refusal is the one the design keeps")
	}
	if la.unhostedOutcome(refusal, unhosted{}) {
		t.Error("a hosted run swallowed a terminal-check refusal")
	}
	if la.unhostedOutcome(refusal, unhosted{reason: standaloneNoDaemon}) {
		t.Error("a run standalone for a missing daemon swallowed a refusal no daemon could have sent")
	}
}

func TestUnhostedOutcome_AnnouncesADegradeNoBlockCovered(t *testing.T) {
	var lines []string
	la := &LocalAdmission{
		PipelineClient: true,
		Logf:           func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	err := fmt.Errorf("lease: %w", wingdclient.ErrDaemonLacksOperation)
	for range 3 {
		if !la.unhostedOutcome(err, unhosted{hosted: true}) {
			t.Fatal("a hosted run did not degrade on an operation the daemon does not serve")
		}
	}
	if len(lines) != 1 {
		t.Fatalf("degrade announced %d times, want exactly 1: %v", len(lines), lines)
	}
	for _, want := range []string{"without local coordination", "stays with the daemon"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("line %q omits %q", lines[0], want)
		}
	}
}

func TestUnhostedOutcome_SaysNothingWhenABlockAlreadyDid(t *testing.T) {
	var lines []string
	la := &LocalAdmission{
		PipelineClient: true,
		Logf:           func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	for range 3 {
		if !la.unhostedOutcome(fmt.Errorf("local admission: %w", wingdclient.ErrNoDaemonHost),
			unhosted{reason: standaloneNoDaemon}) {
			t.Fatal("implicit run did not degrade")
		}
	}
	if len(lines) != 0 {
		t.Fatalf("degrade printed %d line(s) beside the run's own standalone block: %v", len(lines), lines)
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

func TestReservedNodeAdmissionUsesExistingLease(t *testing.T) {
	la := NewReservedNodeAdmission("/unused", "test", "reserved-token", wingwire.OriginController)
	ctx, ok := la.attachReservedNode(context.Background(), "test-run", 37)
	if !ok {
		t.Fatal("reserved admission was not attached")
	}
	got, token, hostAdmitted := localAdmissionFromContext(ctx)
	if got != la || token != "reserved-token" || !hostAdmitted {
		t.Fatalf("admission context = (%p, %q, %v), want (%p, reserved-token, true)", got, token, hostAdmitted, la)
	}
	if child := localAdmissionChildTokenFromContext(ctx); child != "reserved-token" {
		t.Fatalf("child token = %q, want reserved-token", child)
	}
	if priority := localAdmissionPriorityFromContext(ctx); priority != 37 {
		t.Fatalf("priority = %d, want 37", priority)
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

// NewReservedNodeAdmission attaches node execution to capacity that a caller
// already reserved. The caller retains ownership of the lease and must release
// it after execution stops.
func NewReservedNodeAdmission(home, version, leaseToken string, origin wingwire.Origin) *LocalAdmission {
	return &LocalAdmission{
		Home:                   home,
		Version:                version,
		Origin:                 origin,
		reservedNodeLeaseToken: leaseToken,
	}
}
