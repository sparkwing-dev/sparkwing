package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// contendedRun folds one contended run of pipeline "ci" (plan hash B) that
// measured peakCores while admitted at charge, mirroring the end-of-run fold
// recordRunProfile performs for a daemon-flagged run. key is the profile
// identity the fold lands on; the run row keeps the bare pipeline name, as
// it does in production.
func contendedRun(t *testing.T, st *store.Store, ctx context.Context, key, runID string, peakCores float64, charge runCharge) {
	t.Helper()
	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "ci", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, runID, "build", store.MetricSample{
		TS: start, CPUMillicores: int64(peakCores * 1000), MemoryBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}
	recordRunProfile(ctx, st, key, runID, nil, "B", charge, true, start, start.Add(time.Second))
}

// gitRepoDir makes an empty checkout-shaped directory named name, so a
// process running inside it keys its profiles under that repo.
func gitRepoDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPoisonedFloorRecoversWithoutManualReset reproduces the BW-849 drill: a
// pipeline re-measuring after a structural change ratchets its charge under
// external load (ceiling hits double the demand floor until the grantable
// ceiling caps it), and then keeps being flagged contended while the load
// tails off. The runs' own measurements prove demand is ~1 core, so the
// charge must converge back near it instead of pricing the pipeline at the
// whole machine until an operator finds `runs stats --reset`.
func TestPoisonedFloorRecoversWithoutManualReset(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	const grantable = 7.5
	charge := func() float64 {
		prof, err := st.GetPipelineProfile(ctx, "ci", "")
		if err != nil {
			t.Fatal(err)
		}
		res := capacity.Resolve(nil, prof, 8, "B")
		res, _ = capacity.ApplyHostCeiling(res, "ci", 8, grantable, 30<<30)
		return res.Cores
	}

	// External load: each contended run consumes its whole charge (the load,
	// not the pipeline), so ceiling hits escalate the floor run over run.
	for i := range 4 {
		c := charge()
		contendedRun(t, st, ctx, "ci", fmt.Sprintf("hot%d", i), c, runCharge{Cores: c})
	}
	if c := charge(); c < grantable {
		t.Fatalf("charge = %v cores, want the ratchet to reach the grantable ceiling %v first", c, grantable)
	}

	// Load gone, runs still flagged contended: every measurement now says the
	// pipeline wants ~1 core and never hits its admitted ceiling.
	for i := range 6 {
		contendedRun(t, st, ctx, "ci", fmt.Sprintf("calm%d", i), 1.0, runCharge{Cores: charge()})
	}

	if c := charge(); c > capacity.SafetyMultiple*1.0 {
		t.Errorf("charge = %v cores after six 1-core contended runs, want <= %v (floor decays toward evidence instead of poisoning the profile)",
			c, capacity.SafetyMultiple*1.0)
	}
}

// TestContentionInOneRepoLeavesAnothersPricingAlone reproduces the second
// BW-849 drill against the profile identity production derives: one machine,
// two checkouts, each with a pipeline called "ci". Contention that ratchets
// the first repo's demand floor to the machine ceiling must not price the
// second repo's runs, which have never been measured at all.
func TestContentionInOneRepoLeavesAnothersPricingAlone(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	const grantable = 7.5
	// resolve prices "ci" exactly as admission does from the current
	// directory: the stored profile read under the key this process derives.
	resolve := func() capacity.Resolution {
		t.Helper()
		key := currentProfileKey("ci")
		prof, err := st.GetPipelineProfile(ctx, key, "")
		if err != nil {
			t.Fatal(err)
		}
		res := capacity.Resolve(nil, prof, 8, "B")
		res, _ = capacity.ApplyHostCeiling(res, key, 8, grantable, 30<<30)
		return res
	}

	t.Chdir(gitRepoDir(t, "alpha"))
	for i := range 4 {
		c := resolve().Cores
		contendedRun(t, st, ctx, currentProfileKey("ci"), fmt.Sprintf("alpha%d", i), c, runCharge{Cores: c})
	}
	if res := resolve(); res.Source != store.CostSourceFloor || res.Cores < grantable {
		t.Fatalf("alpha priced %v cores from %q, want the contended floor ratcheted to the grantable ceiling %v first",
			res.Cores, res.Source, grantable)
	}

	t.Chdir(gitRepoDir(t, "beta"))
	res := resolve()
	if res.Source != store.CostSourceDefault {
		t.Errorf("beta priced %v cores from %q, want the cold-start default: alpha's contention poisoned an unmeasured pipeline in another repo",
			res.Cores, res.Source)
	}
	if res.Cores != 4 {
		t.Errorf("beta charged %v cores, want the 4-core cold-start half of an 8-core machine", res.Cores)
	}
}
