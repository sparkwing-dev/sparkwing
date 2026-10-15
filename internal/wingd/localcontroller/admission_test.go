package localcontroller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAdmissionUsesOwnedStoreDuringDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var binding Binding
	owner, err := (Config{Bind: func(_ context.Context, b Binding) (Services, error) {
		binding = b
		seedOwner(t, b, "run", false)
		return serviceFixture(b), nil
	}}).Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := owner.Ready(); err == nil {
		t.Fatal("store became ready before reconciliation")
	}
	if err := owner.FinalizeCancelledRuns([]string{"run"}, "before readiness"); err == nil {
		t.Fatal("cancellation acquired authority before reconciliation")
	}
	if _, err := owner.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.Ready(); err != nil {
		t.Fatal(err)
	}
	if terminal, err := owner.IsRunTerminal("run"); terminal || err != nil {
		t.Fatalf("live run: terminal=%t error=%v", terminal, err)
	}
	if _, err := binding.Store.DB().ExecContext(t.Context(), `UPDATE triggers SET status='claimed',claimed_by='owner',capability_prefix='owner',lease_expires_at=?,started_at=1 WHERE id='run'`, time.Now().Add(limits.DefaultLease).UnixNano()); err != nil {
		t.Fatal(err)
	}
	cancel()
	owner.FinalizeRun("run")
	row, err := binding.Tenant.ReadRun(t.Context(), "run")
	if err != nil || row.CancelRequestedAt == nil || row.CancelReason == "" {
		t.Fatalf("disconnect during drain: cancelled=%v reason=%q: %v", row.CancelRequestedAt, row.CancelReason, err)
	}
	seq := row.StateSeq
	if err := owner.FinalizeCancelledRuns([]string{"run", "run"}, "replacement reason"); err != nil {
		t.Fatal(err)
	}
	row, err = binding.Tenant.ReadRun(t.Context(), "run")
	if err != nil || row.StateSeq != seq {
		t.Fatalf("repeated cancellation changed sequence: %d != %d: %v", row.StateSeq, seq, err)
	}
	c := owner.(*localController)
	if err := c.sweep(t.Context(), c.lease); err != nil {
		t.Fatal(err)
	}
	if terminal, err := owner.IsRunTerminal("run"); terminal || err != nil {
		t.Fatalf("cancellation ended a live owner: terminal=%t error=%v", terminal, err)
	}
	if _, err := binding.Store.DB().ExecContext(t.Context(), `UPDATE triggers SET lease_expires_at=1 WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	if err := c.sweep(t.Context(), c.lease); err != nil {
		t.Fatal(err)
	}
	if terminal, err := owner.IsRunTerminal("run"); !terminal || err != nil {
		t.Fatalf("cancelled run: terminal=%t error=%v", terminal, err)
	}
	if err := owner.FinalizeCancelledRuns([]string{"run"}, "after finish"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.IsRunTerminal("missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Ready(); err == nil {
		t.Fatal("closed store remained ready")
	}
	if _, err := owner.IsRunTerminal("run"); err == nil {
		t.Fatal("closed store answered a run read")
	}
	if err := owner.FinalizeCancelledRuns([]string{"run"}, "after close"); err == nil {
		t.Fatal("closed controller accepted cancellation")
	}
}

func FuzzAdmissionCancellationKeepsTenantAndFence(f *testing.F) {
	f.Add(uint8(0), uint8(0), uint8(2))
	f.Add(uint8(1), uint8(1), uint8(3))
	f.Add(uint8(2), uint8(2), uint8(4))
	f.Fuzz(func(t *testing.T, mode, target, suffix uint8) {
		var binding Binding
		own, foreign := fmt.Sprintf("local-%d", suffix), fmt.Sprintf("foreign-%d", suffix)
		c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
			binding = b
			seedOwner(t, b, own, false)
			_, err := b.Store.DB().ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := b.Store.ForTeam(t.Context(), "foreign")
			if err != nil {
				t.Fatal(err)
			}
			seedOwner(t, Binding{Store: b.Store, Tenant: tenant}, foreign, false)
			return serviceFixture(b), nil
		})
		if _, err := c.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		foreignTenant, err := binding.Store.ForTeam(t.Context(), "foreign")
		if err != nil {
			t.Fatal(err)
		}
		before, err := foreignTenant.ReadRun(t.Context(), foreign)
		if err != nil {
			t.Fatal(err)
		}
		switch mode % 3 {
		case 1:
			_, err := binding.Store.DB().ExecContext(t.Context(), `UPDATE leases SET until=1 WHERE name='controller.reaper'`)
			if err != nil {
				t.Fatal(err)
			}
		case 2:
			_, err := binding.Store.DB().ExecContext(t.Context(), `UPDATE leases SET generation=generation+?,holder='successor' WHERE name='controller.reaper'`, int64(suffix)+1)
			if err != nil {
				t.Fatal(err)
			}
		}
		id := []string{own, foreign, "missing"}[target%3]
		err = c.FinalizeCancelledRuns([]string{id}, "cancelled")
		allowed := mode%3 == 0 && target%3 == 0
		if (err == nil) != allowed {
			t.Fatalf("mode=%d target=%s: %v", mode%3, id, err)
		}
		row, readErr := binding.Tenant.ReadRun(t.Context(), own)
		if readErr != nil || (row.CancelRequestedAt != nil) != allowed {
			t.Fatalf("local cancellation: cancelled=%v allowed=%t: %v", row.CancelRequestedAt, allowed, readErr)
		}
		row, readErr = foreignTenant.ReadRun(t.Context(), foreign)
		if readErr != nil || row.CancelRequestedAt != nil || row.StateSeq != before.StateSeq {
			t.Fatalf("foreign cancellation: cancelled=%v sequence=%d != %d: %v", row.CancelRequestedAt, row.StateSeq, before.StateSeq, readErr)
		}
	})
}
