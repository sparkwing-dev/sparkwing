package store_test

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func profileCompletionFixture(t *testing.T) (*store.Store, *store.Tenant, store.EffectDelivery) {
	t.Helper()
	return profileCompletionFixtureWith(t, storetest.Open(t))
}

func profileCompletionFixtureWith(t *testing.T, s *store.Store) (*store.Store, *store.Tenant, store.EffectDelivery) {
	t.Helper()
	tenant, finish := runCompletionFixture(t, s)
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "completion", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	due, err := s.AsOperator().DueProfileFolds(t.Context(), lease, 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("profile effects=%+v: %v", due, err)
	}
	effect := due[0]
	return s, tenant, store.EffectDelivery{RunID: effect.RunID, Effect: effect.Effect, Revision: effect.DesiredRev, Lease: lease}
}

func TestProfileCompletionFoldsAndAcknowledgesAtomicallySQLite(t *testing.T) {
	s, tenant, delivery := profileCompletionFixtureWith(t, storetest.OpenSQLite(t))
	observations := map[string]store.ProfileObservation{"": {CPUMeasured: true, PeakCores: 1}, "job": {CPUMeasured: true, PeakCores: 2}}
	if _, err := s.DB().Exec(`CREATE TRIGGER refuse_profile_ack BEFORE UPDATE ON run_effects WHEN NEW.effect='profile_fold' BEGIN SELECT RAISE(ABORT,'ack refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ApplyProfileFold(t.Context(), delivery, observations); err == nil {
		t.Fatal("fold committed without its acknowledgement")
	}
	if profile, err := tenant.GetPipelineProfile(t.Context(), "fixture", "job"); err != nil || profile != nil {
		t.Fatalf("failed fold changed profile: %+v: %v", profile, err)
	}
	if _, err := s.DB().Exec(`DROP TRIGGER refuse_profile_ack`); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		applied, err := tenant.ApplyProfileFold(t.Context(), delivery, observations)
		if err != nil || applied != (i == 0) {
			t.Fatalf("fold %d applied=%v: %v", i, applied, err)
		}
	}
	run, err := tenant.ReadRun(t.Context(), delivery.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for node, obs := range observations {
		p, err := tenant.GetPipelineProfile(t.Context(), run.Pipeline, node)
		if err != nil || p == nil || p.SampleCount != 1 || p.PeakCores != obs.PeakCores {
			t.Fatalf("profile %q=%+v: %v", node, p, err)
		}
	}
	due, err := s.AsOperator().DueProfileFolds(t.Context(), delivery.Lease, 1)
	if err != nil || len(due) != 0 {
		t.Fatalf("delivered fold remains due: %+v: %v", due, err)
	}
	effects, err := s.AsOperator().DueRunEffects(t.Context(), delivery.Lease, 100)
	if err != nil || len(effects) != 3 {
		t.Fatalf("fold consumed another effect: %+v: %v", effects, err)
	}
}

func TestProfileCompletionCannotOutliveLeaseOrCrossTenantProperty(t *testing.T) {
	s, tenant, delivery := profileCompletionFixture(t)
	other := tenantFor(t, s, "other")
	random := rand.New(rand.NewPCG(17, 53))
	for sample := range 64 {
		forged, target := delivery, tenant
		switch sample % 6 {
		case 0:
			forged.Lease.Generation += 1 + random.Int64N(1000)
		case 1:
			forged.Lease.Holder += "-foreign"
		case 2:
			forged.Lease.Name = "controller.owner"
		case 3:
			target = other
		case 4:
			forged.Revision += 1 + random.Int64N(1000)
		case 5:
			forged.Effect = store.EffectMetrics
		}
		if applied, err := target.ApplyProfileFold(t.Context(), forged, map[string]store.ProfileObservation{"job": {CPUMeasured: true, PeakCores: 1}}); err == nil || applied {
			t.Fatalf("forged fold %d=%v: %v", sample, applied, err)
		}
		for _, scoped := range []*store.Tenant{tenant, other} {
			if p, err := scoped.GetPipelineProfile(t.Context(), "fixture", "job"); err != nil || p != nil {
				t.Fatalf("forged fold changed %s profile: %+v: %v", scoped.Team(), p, err)
			}
		}
	}
	due, err := s.AsOperator().DueProfileFolds(t.Context(), delivery.Lease, 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("refused fold changed delivery: %+v: %v", due, err)
	}
}

func TestProfileCompletionInvalidSiblingRollsBackBatch(t *testing.T) {
	s, tenant, delivery := profileCompletionFixture(t)
	observations := map[string]store.ProfileObservation{"a": {CPUMeasured: true, PeakCores: 1}, "z": {CPUMeasured: true, PeakMemoryBytes: -1}}
	if applied, err := tenant.ApplyProfileFold(t.Context(), delivery, observations); err == nil || applied {
		t.Fatalf("invalid batch=%v: %v", applied, err)
	}
	if p, err := tenant.GetPipelineProfile(t.Context(), "fixture", "a"); err != nil || p != nil {
		t.Fatalf("partial batch committed: %+v: %v", p, err)
	}
	due, err := s.AsOperator().DueProfileFolds(t.Context(), delivery.Lease, 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("failed batch consumed work: %+v: %v", due, err)
	}
}

func TestProfileCompletionCannotCommitAfterLeaseExpires(t *testing.T) {
	s, tenant, delivery := profileCompletionFixtureWith(t, storetest.OpenSQLite(t))
	s.SetTestClock(t, func() time.Time { return delivery.Lease.ExpiresAt })
	if applied, err := tenant.ApplyProfileFold(t.Context(), delivery, map[string]store.ProfileObservation{"job": {CPUMeasured: true, PeakCores: 1}}); err == nil || applied {
		t.Fatalf("expired fold=%v: %v", applied, err)
	}
}

func TestProfileCompletionConcurrentReplaysFoldOnce(t *testing.T) {
	_, tenant, delivery := profileCompletionFixture(t)
	observations := map[string]store.ProfileObservation{"job": {CPUMeasured: true, PeakCores: 1}}
	var wins atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		workers.Go(func() {
			<-start
			applied, err := tenant.ApplyProfileFold(t.Context(), delivery, observations)
			if err != nil {
				t.Error(err)
				return
			}
			if applied {
				wins.Add(1)
			}
		})
	}
	close(start)
	workers.Wait()
	p, err := tenant.GetPipelineProfile(t.Context(), "fixture", "job")
	if err != nil || wins.Load() != 1 || p == nil || p.SampleCount != 1 {
		t.Fatalf("concurrent fold wins=%d profile=%+v: %v", wins.Load(), p, err)
	}
}
