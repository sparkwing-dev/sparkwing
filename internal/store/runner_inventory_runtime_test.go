package store_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestRunnerInventoryWithSealedAdmissionAndEnrollment(t *testing.T) {
	f := newStoreRecoveryFixture(t, false)
	ctx := t.Context()
	claim, err := f.s.ResolveAuthority(ctx, f.holder.Authority)
	if err != nil || claim.Capability.Parent == nil {
		t.Fatalf("claim authority: %+v %v", claim, err)
	}
	if err := f.s.RecordRunnerPoll(ctx, store.UnitDemand{CapabilityPrefix: claim.Capability.Parent.ID, Labels: []string{"gpu", "name=host", "pool=prod"}, Available: match.Resources{Cores: 2.5, MemoryBytes: 2048}}); err != nil {
		t.Fatal(err)
	}
	before, err := f.tenant.ReadRunners(ctx)
	if err != nil || len(before.Runners) != 2 {
		t.Fatalf("enrolled inventory: %+v %v", before, err)
	}
	rows := map[string]int{}
	for i, row := range before.Runners {
		rows[row.Runner.Name] = i
	}
	host, runner := before.Runners[rows["host"]], before.Runners[rows["runner"]]
	if !reflect.DeepEqual(host.ActiveJobs, []string{f.ref.RunID}) || !reflect.DeepEqual(runner.ActiveJobs, []string{f.ref.RunID + "/" + f.ref.NodeID}) || !reflect.DeepEqual(runner.Runner.Labels, []string{"gpu"}) || runner.Headroom == nil || runner.Headroom.Cores != 2.5 || runner.Headroom.MemoryBytes != 2048 || host.Headroom != nil {
		t.Fatalf("authority-bound observations: %+v", before)
	}
	if _, err := f.tenant.RenewNodeClaim(ctx, f.ref, f.holder); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.RenewOwner(ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	after, err := f.tenant.ReadRunners(ctx)
	if err != nil || len(after.Runners) != 2 {
		t.Fatalf("renewed inventory: %+v %v", after, err)
	}
	for _, row := range after.Runners {
		old := before.Runners[rows[row.Runner.Name]]
		if row.LastSeen.Before(old.LastSeen) || !reflect.DeepEqual(row.Headroom, old.Headroom) || !reflect.DeepEqual(row.ActiveJobs, old.ActiveJobs) {
			t.Fatalf("renewal changed identity or capacity: old=%+v new=%+v", old, row)
		}
	}
}
