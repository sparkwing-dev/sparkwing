package store

import (
	"errors"
	"testing"
	"time"
)

func TestProfileCompletionRollsBackWhenLeaseLapsesDuringFold(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunSuccess)
	maintenanceEffects(t, s, RunSuccess)
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "completion", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	delivery := EffectDelivery{RunID: "run", Effect: EffectProfileFold, Revision: 1, Lease: lease}
	err = tenant.deliverRunEffect(t.Context(), delivery, nil, func(tx *storeTx) error {
		if err := tenant.recordProfileObservationTx(t.Context(), tx, "ci", "job", ProfileObservation{CPUMeasured: true, PeakCores: 2}); err != nil {
			return err
		}
		*now = lease.ExpiresAt
		return nil
	})
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired commit=%v", err)
	}
	if p, err := tenant.GetPipelineProfile(t.Context(), "ci", "job"); err != nil || p != nil {
		t.Fatalf("expired fold changed profile: %+v: %v", p, err)
	}
	next, err := s.AsOperator().AcquireSingletonLease(t.Context(), "completion", "next", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := s.AsOperator().DueProfileFolds(t.Context(), next, 1)
	if err != nil || len(effects) != 1 || effects[0].DeliveredRev != 0 {
		t.Fatalf("expired fold acknowledged: %+v: %v", effects, err)
	}
}
