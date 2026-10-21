package localcontroller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type scheduleLauncher struct {
	launched []store.CronSchedule
}

func (l *scheduleLauncher) Launch(_ context.Context, row store.CronSchedule, _ time.Time) (string, error) {
	l.launched = append(l.launched, row)
	return "run", nil
}

func (*scheduleLauncher) Active(context.Context, store.CronSchedule, string, time.Duration) (bool, error) {
	return false, nil
}

func TestScheduleFactoryUsesOwnedStoreForPinsAndTicks(t *testing.T) {
	var binding Binding
	openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		return serviceFixture(b), nil
	})
	launcher := &scheduleLauncher{}
	service, err := NewScheduleService(binding, "v0.71.0", launcher)
	if err != nil {
		t.Fatal(err)
	}
	if *service.Operator != *binding.Store.AsOperator() || service.Schedules != binding.Tenant || service.Side != store.CronWhereLocal || service.PinRoot != filepath.Join(binding.Home, "crons") || service.LockPath != filepath.Join(binding.Home, "crons.lock") {
		t.Fatal("schedule factory replaced the elected store or its home")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sparkwing.yaml"), []byte(`pipelines:
  - name: ci
    entrypoint: CI
    on:
      schedule:
        - cron: "* * * * *"
          where: local
`), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled := filepath.Join(t.TempDir(), "compiled")
	if err := os.WriteFile(compiled, []byte("pipeline fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	service.Now = func() time.Time { return now }
	armed, err := service.Arm(t.Context(), root, crons.ArmOptions{Prove: func(context.Context, string, string) (crons.Proof, error) {
		return crons.Proof{Binary: compiled}, nil
	}})
	if err != nil || len(armed.Schedules) != 1 {
		t.Fatalf("registration: %+v: %v", armed, err)
	}
	row := armed.Schedules[0]
	if row.LockedBinary == "" || row.LockedDigest == "" || row.ArmedBy != service.ArmedBy {
		t.Fatal("registration lost its private pin or local account")
	}
	if _, err := service.Unlock(t.Context(), row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Lock(t.Context(), row.ID, func(context.Context, string, string) (crons.Proof, error) {
		return crons.Proof{Binary: compiled}, nil
	}); err != nil {
		t.Fatal(err)
	}
	seedOwner(t, binding, "run", false)
	now = now.Add(time.Minute)
	tick, err := service.Tick(t.Context(), false)
	if err != nil || tick.Fired != 1 || len(tick.Errors) != 0 || len(launcher.launched) != 1 {
		t.Fatalf("tick: %+v: %v", tick, err)
	}
	fires, err := binding.Tenant.ListCronFires(t.Context(), row.ID, 0)
	if err != nil || len(fires) != 1 || fires[0].RunID != "run" {
		t.Fatalf("committed fire history: %+v: %v", fires, err)
	}
	if err := service.Disarm(t.Context(), row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(row.LockedBinary); !os.IsNotExist(err) {
		t.Fatalf("disarm retained its private pin: %v", err)
	}
}

func TestScheduleFactoryRequiresControllerOwnedBindings(t *testing.T) {
	var binding Binding
	openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		return serviceFixture(b), nil
	})
	for _, mutate := range []func(*Binding){
		func(b *Binding) { b.Store = nil },
		func(b *Binding) { b.Tenant = nil },
		func(b *Binding) { b.Controller = nil },
		func(b *Binding) { b.Home = "relative" },
	} {
		b := binding
		mutate(&b)
		if service, err := NewScheduleService(b, "v0.71.0", &scheduleLauncher{}); service != nil || err == nil {
			t.Fatal("schedule factory accepted incomplete ownership")
		}
	}
	if _, err := NewScheduleService(binding, "v0.71.0", nil); err == nil {
		t.Fatal("schedule factory accepted a missing sealed-intake launcher")
	}
}
