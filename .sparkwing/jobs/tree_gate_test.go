package jobs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func treeGateRepo(t *testing.T) string {
	t.Helper()
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	t.Setenv("SPARKWING_ENGINE_VERSION", "v0.0.1")
	t.Setenv("SPARKWING_COMMIT", "")
	t.Setenv("SPARKWING_RUNNER_NAME", "workstation")
	return root
}

func TestRecordTreeOutputsAPassOnlyForACleanUnmovedCheckout(t *testing.T) {
	root := treeGateRepo(t)
	ctx := newTestContext(t)
	pass, err := recordTreePass(ctx, gateAffected)
	if err != nil || !pass.Recorded || pass.Tree == "" || pass.Runner != "workstation" || pass.Mode != gateAffected {
		t.Fatalf("clean checkout pass = %+v, %v", pass, err)
	}
	key, err := currentGateKey(ctx)
	if err != nil || key != pass.gateKey {
		t.Fatalf("recorded key %+v, current key %+v (%v)", pass.gateKey, key, err)
	}

	t.Setenv("SPARKWING_COMMIT", strings.Repeat("0", 40))
	if pass, err := recordTreePass(ctx, gateAffected); err != nil || pass.Recorded || !strings.Contains(pass.Reason, "moved") {
		t.Fatalf("moved HEAD pass = %+v, %v", pass, err)
	}
	t.Setenv("SPARKWING_COMMIT", "")
	writeGoFile(t, filepath.Join(root, "internal", "sound.go"), "package internal\n\nfunc Sound() int { return 2 }\n")
	if pass, err := recordTreePass(ctx, gateAffected); err != nil || pass.Recorded || !strings.Contains(pass.Reason, "uncommitted") {
		t.Fatalf("dirty checkout pass = %+v, %v", pass, err)
	}
	gitCommitAll(t, root, "dirty work committed")
	t.Setenv("SPARKWING_ENGINE_VERSION", "")
	if pass, err := recordTreePass(ctx, gateAffected); err != nil || pass.Recorded {
		t.Fatalf("unknown engine pass = %+v, %v", pass, err)
	}
}

func TestRecordTreeRecordsOnlyWhenBothGateJobsRanInItsRun(t *testing.T) {
	treeGateRepo(t)
	ran := json.RawMessage("true")
	record := &recordTree{Gate: sparkwing.Ref[bool]{JobID: "gate"}, Race: sparkwing.Ref[bool]{JobID: "race-touched"}, mode: gateFull}
	for name, tc := range map[string]struct {
		outputs map[string]json.RawMessage
		want    bool
	}{
		"both ran":       {outputs: map[string]json.RawMessage{"gate": ran, "race-touched": ran}, want: true},
		"gate skipped":   {outputs: map[string]json.RawMessage{"race-touched": ran}},
		"race skipped":   {outputs: map[string]json.RawMessage{"gate": ran}},
		"both skipped":   {},
		"unreadable":     {outputs: map[string]json.RawMessage{"gate": json.RawMessage(`"yes"`), "race-touched": ran}},
		"reported false": {outputs: map[string]json.RawMessage{"gate": json.RawMessage("false"), "race-touched": ran}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := newTestHost(t, sparkwingtest.Options{Outputs: tc.outputs}).Context(t.Context())
			pass, err := record.record(ctx)
			if err != nil || pass.Recorded != tc.want {
				t.Fatalf("pass = %+v, %v; want recorded %v", pass, err, tc.want)
			}
			if !tc.want && !strings.Contains(pass.Reason, "did not run") {
				t.Fatalf("reason %q does not say which job did not run", pass.Reason)
			}
		})
	}
}

func TestOnlyAFullPassProvesItsTreeGateAndEngine(t *testing.T) {
	key := gateKey{Tree: "tree", Fingerprint: "gate", Engine: "v1"}
	pass := gatePass{gateKey: key, Recorded: true, Mode: gateFull}
	if ok, why := passProves(pass, key); !ok {
		t.Fatalf("matching full pass refused: %s", why)
	}
	for name, mutate := range map[string]func(*gatePass){
		"unrecorded":     func(p *gatePass) { p.Recorded = false },
		"another tree":   func(p *gatePass) { p.Tree = "other" },
		"another gate":   func(p *gatePass) { p.Fingerprint = "other" },
		"another engine": func(p *gatePass) { p.Engine = "v2" },
		"affected mode":  func(p *gatePass) { p.Mode = gateAffected },
	} {
		changed := pass
		mutate(&changed)
		if ok, _ := passProves(changed, key); ok {
			t.Errorf("%s: proved the tree", name)
		}
	}
}

func TestTheGateSkipsOnlyWhenTheProvenLookupReturnsAFullMatchingPass(t *testing.T) {
	treeGateRepo(t)
	key, err := currentGateKey(newTestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		latest func(context.Context, string, string) (json.RawMessage, error)
		skip   bool
	}{
		"matching full pass": {latest: passFor(gatePass{gateKey: key, Recorded: true, Mode: gateFull}), skip: true},
		"affected pass":      {latest: passFor(gatePass{gateKey: key, Recorded: true, Mode: gateAffected})},
		"another tree":       {latest: passFor(gatePass{gateKey: gateKey{Tree: "other", Fingerprint: key.Fingerprint, Engine: key.Engine}, Recorded: true, Mode: gateFull})},
		"no proven run": {latest: func(context.Context, string, string) (json.RawMessage, error) {
			return nil, sparkwing.ErrRefAbsent
		}},
		"lookup refused": {latest: func(context.Context, string, string) (json.RawMessage, error) {
			return nil, context.DeadlineExceeded
		}},
	} {
		t.Run(name, func(t *testing.T) {
			var asked []string
			latest := tc.latest
			host := newTestHost(t, sparkwingtest.Options{Latest: func(ctx context.Context, pipeline, job string) (json.RawMessage, error) {
				asked = append(asked, pipeline+"/"+job)
				return latest(ctx, pipeline, job)
			}})
			ctx := host.Context(newTestContext(t))
			if got := skipWhenTreePassed(provenGatePass("gate"))(ctx); got != tc.skip {
				t.Fatalf("skip = %v, want %v", got, tc.skip)
			}
			if len(asked) != 1 || asked[0] != "gate/"+recordTreeJob {
				t.Fatalf("lookups = %v, want gate/%s", asked, recordTreeJob)
			}
		})
	}
}

func passFor(pass gatePass) func(context.Context, string, string) (json.RawMessage, error) {
	return func(context.Context, string, string) (json.RawMessage, error) { return json.Marshal(pass) }
}

func TestEveryGateJobDeclaresTheProvenLookup(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&Gate{}).Plan(t.Context(), plan, GateArgs{}, sparkwing.RunContext{Pipeline: "gate"})
	})
	for _, job := range plan.Jobs {
		if len(job.References) != 1 {
			t.Fatalf("%s references = %+v, want the proven gate lookup", job.ID, job.References)
		}
		ref := job.References[0]
		if ref.Pipeline != "gate" || ref.Job != recordTreeJob || !ref.FromProvenRun || ref.FromAnyRun {
			t.Errorf("%s reference = %+v, want a proven-only read of gate/%s", job.ID, ref, recordTreeJob)
		}
	}
}

func TestGateModeFollowsTheFullFlag(t *testing.T) {
	for _, tc := range []struct {
		in   GateArgs
		want gateMode
	}{{GateArgs{}, gateAffected}, {GateArgs{Full: true}, gateFull}} {
		g := &Gate{}
		inspectPlan(t, func(plan *sparkwing.Plan) error {
			return g.Plan(t.Context(), plan, tc.in, sparkwing.RunContext{Pipeline: "gate"})
		})
		if g.mode != tc.want {
			t.Errorf("Plan(%+v) mode = %q, want %q", tc.in, g.mode, tc.want)
		}
	}
}

func TestChangingThePipelineModuleChangesTheFingerprint(t *testing.T) {
	root := treeGateRepo(t)
	ctx := newTestContext(t)
	before, err := currentGateKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs.go"), "package pipelines\n\nfunc Jobs() int { return 2 }\n")
	gitCommitAll(t, root, "change the gate")
	after, err := currentGateKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("changing the pipeline module kept the gate fingerprint")
	}
}
