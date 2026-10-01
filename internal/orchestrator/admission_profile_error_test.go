package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type failedAdmissionProfileStore struct {
	StateBackend
	err error
}

func (s failedAdmissionProfileStore) GetPipelineProfile(context.Context, string, string) (*store.PipelineProfile, error) {
	return nil, s.err
}

func TestAdmissionReturnsProfileReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admit func(context.Context, *LocalAdmission, Backends, *sparkwing.Plan) error
	}{
		{"node", func(ctx context.Context, a *LocalAdmission, b Backends, p *sparkwing.Plan) error {
			_, err := a.admitNode(ctx, b, "parcel", "run", "pack", p.Job("pack"), 0)
			return err
		}},
		{"node with semaphore", func(ctx context.Context, a *LocalAdmission, b Backends, p *sparkwing.Plan) error {
			_, err := a.acquireNodeHostSlot(ctx, b, "parcel", "run", "pack", p.Job("pack"), wingwire.SemaphoreClaim{Name: "parcel", Capacity: 1, Cost: 1}, 0, nil)
			return err
		}},
		{"pinned run", func(ctx context.Context, a *LocalAdmission, b Backends, p *sparkwing.Plan) error {
			p.Resources(sparkwing.Cores(2))
			_, _, err := a.admitRun(ctx, b, "parcel", "run", p, 1, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readErr := errors.New("profile database read failed")
			backends := Backends{State: failedAdmissionProfileStore{err: readErr}, LocalCoordination: true}
			spawned := false
			admission := &LocalAdmission{Home: t.TempDir(), Spawn: func(string, string) error {
				spawned = true
				return errors.New("unexpected daemon startup")
			}}
			plan := sparkwing.NewPlan()
			sparkwing.Job(plan, "pack", func(context.Context) error { return nil })
			if err := tc.admit(t.Context(), admission, backends, plan); !errors.Is(err, readErr) {
				t.Errorf("admission error = %v, want original profile read failure", err)
			}
			if spawned {
				t.Error("admission attempted daemon startup after profile read failed")
			}
		})
	}
}

func TestAdmissionMissingProfileUsesDefault(t *testing.T) {
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "pack", func(context.Context) error { return nil })
	admission := &LocalAdmission{Home: t.TempDir()}
	backends := Backends{State: failedAdmissionProfileStore{}, LocalCoordination: true}
	charge, profile, _, err := admission.resolveNodeHostCost(t.Context(), backends, "parcel", "pack", node)
	if err != nil {
		t.Fatal(err)
	}
	if profile != nil || charge.Source != store.CostSourceDefault || charge.Cores <= 0 {
		t.Fatalf("missing profile resolved to profile=%+v charge=%+v, want a positive default charge", profile, charge)
	}
}
