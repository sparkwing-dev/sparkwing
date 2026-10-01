package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type nodeProfileStore struct {
	StateBackend
	profile *store.PipelineProfile
}

func (s nodeProfileStore) GetPipelineProfile(context.Context, string, string) (*store.PipelineProfile, error) {
	return s.profile, nil
}

func TestNodeProfileVersion(t *testing.T) {
	for _, name := range []string{"matching", "changed", "changed after context"} {
		t.Run(name, func(t *testing.T) {
			plan := fingerprintPlan(2)
			profile := &store.PipelineProfile{
				PlanHash: capacityFingerprint(plan), SampleCount: 3,
				CPUMeasured: true, PeakCores: 1, SustainedCores: 0.5,
				PeakMemoryBytes: 1024,
			}
			if name == "changed" {
				plan = fingerprintPlan(4)
			}
			backends := Backends{State: nodeProfileStore{profile: profile}, LocalCoordination: true}
			admission := &LocalAdmission{Home: t.TempDir(), Spawn: func(string, string) error {
				return errors.New("test has no admission daemon")
			}}
			dispatch := newDispatchState(t.Context(), backends, nil, "run", "demo", plan, nil,
				DebugDirectives{}, "", nil, 0, admission, "", "", false)
			if name == "changed after context" {
				sparkwing.Job(plan, "extra", noopFingerprintJob)
			}
			for _, replaceLease := range []bool{false, true} {
				ctx := dispatch.resolverCtx
				if replaceLease {
					ctx = withLocalAdmission(ctx, admission, "run", "lease", "child", true, 0, runCharge{})
				}
				resolved, _, _, err := admission.resolveNodeHostCost(ctx, backends, "demo", "shard-a", plan.Job("shard-a"))
				if err != nil {
					t.Fatal(err)
				}
				want := store.CostSourceMeasured
				if name != "matching" {
					want = store.CostSourceMeasuring
				}
				if resolved.Source != want {
					t.Errorf("lease replaced=%v: source = %q, want %q", replaceLease, resolved.Source, want)
				}
				if resolved.Cores != 0.5 || resolved.MemoryBytes != 1024 {
					t.Errorf("lease replaced=%v: charge = %+v, want 0.5 cores and 1024 bytes", replaceLease, resolved)
				}
			}
			plan.Job("shard-a").Resources(sparkwing.Cores(2), sparkwing.MemoryGB(1))
			resolved, _, _, err := admission.resolveNodeHostCost(dispatch.resolverCtx, backends, "demo", "shard-a", plan.Job("shard-a"))
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Source != store.CostSourcePin || resolved.Cores != 2 || resolved.MemoryBytes != 1<<30 {
				t.Errorf("explicit pin = %+v, want pinned 2 cores and 1 GiB", resolved)
			}
		})
	}
}
