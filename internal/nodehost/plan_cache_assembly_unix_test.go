//go:build unix

package nodehost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestNativePlanningAssemblesDefaultCacheFromAcquisition(t *testing.T) {
	root, sha := hostSourceCheckout(t)
	environment := []string{"PATH=" + os.Getenv("PATH")}
	seal, err := runspec.EnvironmentDigest(map[string]string{"PATH": os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	config, _, _, _ := nativeHostFixture(t)
	config.Source.Workspace = t.TempDir()
	config.Source.Base = procenv.Base{Local: true, Captured: environment, Masker: config.Masks.masker}
	config.Validation.Placement = nodeproto.PlacementLocal
	planner, _, _, _ := sdkPlanningFixture(t)
	config.Planner = planner
	config.ValidatePlan, config.CanonicalHash = nil, nil
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	owner.Planner.EnsureEngine = planner.EnsureEngine
	owner.Planner.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		process, err := planner.Start(ctx, input, log)
		if process != nil {
			process.(*executorProcess).plan.Jobs[0].Steps = []nodeproto.StepSpec{{ID: "body"}}
		}
		return process, err
	}
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "gh:123", LocalCheckout: root, SHA: sha, PipelineDir: "."}, source.Options{Base: config.Source.Base, BuildEnvironment: func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error) { return environment, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	entry.Engine.Identity = planCacheInputs().Engine
	executable := filepath.Join(t.TempDir(), "sdk")
	if err := os.WriteFile(executable, []byte("compiled entry"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry.Command = []string{executable}
	request := runspec.SealedRequest{RunID: "run", Request: runspec.Request{Team: "team", Pipeline: "build", Env: seal, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:123"}, SHA: sha}}}
	prepare := func() OwnerPlanning {
		t.Helper()
		prepared, err := owner.Planner.PrepareCheckout(t.Context(), CheckoutPlanningInput{Request: request, Entry: entry, Planning: owner.Planning})
		if err != nil {
			t.Fatal(err)
		}
		if _, eligibility, err := PlanCacheIdentity(prepared.Planning.Inputs); err != nil || !eligibility.Eligible {
			t.Fatalf("production cache ineligible: %+v %v", eligibility, err)
		}
		return prepared
	}
	first := prepare()
	cold, err := PlanInitial(t.Context(), first.Planning, first.Functions)
	if err != nil || cold.CacheHit {
		t.Fatalf("cold: %+v %v", cold, err)
	}
	second := prepare()
	warm, err := PlanInitial(t.Context(), second.Planning, second.Functions)
	if err != nil || !warm.CacheHit {
		t.Fatalf("warm: %+v %v", warm, err)
	}
	metadata := filepath.Join(root, ".git", "refs", "tags", "cache-tag")
	if err := os.MkdirAll(filepath.Dir(metadata), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadata, []byte(sha+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tagged := prepare()
	if tagged.Planning.Inputs.SourceDigest == first.Planning.Inputs.SourceDigest {
		t.Fatal("git tag metadata did not invalidate cache")
	}
	if err := os.WriteFile(filepath.Join(root, "dirty-input"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := prepare()
	if changed.Planning.Inputs.SourceDigest == first.Planning.Inputs.SourceDigest {
		t.Fatal("dirty source did not invalidate cache")
	}
	if err := os.WriteFile(executable, []byte("changed executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	rebuilt := prepare()
	if rebuilt.Planning.Inputs.EntryDigest == changed.Planning.Inputs.EntryDigest {
		t.Fatal("changed executable did not invalidate cache")
	}
	entry.Command = []string{"sdk"}
	unresolved, err := owner.Planner.PrepareCheckout(t.Context(), CheckoutPlanningInput{Request: request, Entry: entry, Planning: owner.Planning})
	if err != nil {
		t.Fatal(err)
	}
	if _, eligible, err := PlanCacheIdentity(unresolved.Planning.Inputs); err != nil || eligible.Eligible {
		t.Fatalf("unresolved executable was cacheable: %+v %v", eligible, err)
	}
}

func TestPlanInitialMissingCacheIsNotDisabled(t *testing.T) {
	fixture := newPlanningFixture(t)
	fixture.input.Cache = nil
	if _, err := PlanInitial(context.Background(), fixture.input, fixture.functions()); err == nil || fixture.plans != 0 {
		t.Fatal("missing wiring silently disabled caching")
	}
	fixture.input.Disabled = true
	fixture.input.Cache = fixture.cache
	if _, err := PlanInitial(t.Context(), fixture.input, fixture.functions()); err != nil || fixture.plans != 1 || len(fixture.cache.calls) != 0 {
		t.Fatalf("explicit disable: %v", err)
	}
}
