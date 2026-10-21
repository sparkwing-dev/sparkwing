package nodehost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPlanningDefaults(t *testing.T) {
	for _, secret := range []string{"private-sdk-default", "42"} {
		for _, field := range []string{"job", "group", "step", "concurrency", "safe"} {
			t.Run(secret+"/"+field, func(t *testing.T) { checkPlanningDefaults(t, secret, field) })
		}
	}
}

func FuzzPlanningDefaultsRefuseSecretIdentifiers(f *testing.F) {
	for field := uint8(0); field < 4; field++ {
		for _, value := range []string{"", "\"\\\n", "λ☃", "\x00"} {
			f.Add(field, value)
		}
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		if len(value) > 128 {
			t.Skip()
		}
		secret := "private:" + strings.ToValidUTF8(value, "�")
		checkPlanningDefaults(t, secret, []string{"job", "group", "step", "concurrency"}[field%4])
	})
}

func checkPlanningDefaults(t *testing.T, secret, field string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	job := nodeproto.JobSpec{ID: "safe-job", Steps: []nodeproto.StepSpec{{ID: "body"}}}
	switch field {
	case "job":
		job.ID = "job-" + secret
	case "group":
		job.Groups = []string{"group-" + secret}
	case "step":
		job.Steps[0].ID = "step-" + secret
	case "concurrency":
		job.Concurrency = []nodeproto.ConcurrencySpec{{Group: "group-" + secret}}
	}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
	f := newStoreOwnerFixture(t, openStore(t), runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "build", Mode: runspec.IntakeExecute,
		Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
		Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}},
	})
	resolved := engine.Resolved{Identity: engine.Identity{Version: "v1.0.0", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}}
	directory := t.TempDir()
	f.Host.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
		return &source.Entry{Engine: resolved, Directory: directory}, nil
	}
	accepted := errors.New("stop at accepted plan")
	f.Host.Project = func(store.RunSnapshot) (store.ScheduleSnapshot, error) { return store.ScheduleSnapshot{}, accepted }
	masks := secrets.NewMasker()
	f.Host.Masker, f.Host.Masks = masks, NewMaskRegistry(masks)
	f.Host.Planning.Validation.Placement = nodeproto.PlacementLocal
	var processes []*scriptedSDK
	f.Host.Planner = &SDKPlanner{
		Masker: masks, Log: func(context.Context, nodeproto.FrameLog) error { return nil },
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		},
		Start: func(context.Context, PlanningProcessInput, func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
			process := newPlanningSDK(plan, secret)
			processes = append(processes, process)
			return process, nil
		},
	}
	f.Host.Prepare = func(ctx context.Context, input RunInput, entry *source.Entry) (OwnerPlanning, error) {
		row, err := f.Tenant.ReadRun(ctx, input.RunID)
		if err != nil {
			return OwnerPlanning{}, err
		}
		return f.Host.Planner.PrepareCheckout(ctx, CheckoutPlanningInput{Request: row.Request, Entry: entry, Runner: input.GrantedRunner, Planning: f.Host.Planning})
	}
	_, err := f.Host.Run(ctx, f.Input)
	snapshot, readErr := f.Tenant.ReadSnapshot(ctx, f.Input.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if field == "safe" {
		if !errors.Is(err, accepted) || snapshot.Run.Spec == nil || len(snapshot.Nodes) != 1 {
			t.Fatalf("safe plan was not accepted: %v", err)
		}
		resolved := snapshot.Run.Spec.Resolved
		if resolved.Args["token"] != secret || len(resolved.SecretArgs) != 1 || resolved.SecretArgs[0] != "token" {
			t.Fatal("accepted spec lost SDK defaults or classification")
		}
	} else {
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.SecretInIdentifier {
			t.Fatalf("secret identifier was not refused by the shared validator: %v", err)
		}
		if strings.Contains(err.Error()+failure.Message+failure.Hint+fmt.Sprint(failure.Details), secret) {
			t.Fatal("refusal exposed its secret default")
		}
		if snapshot.Run.State != store.RunFailed || snapshot.Run.Spec != nil || len(snapshot.Run.Plan) != 0 || len(snapshot.Nodes) != 0 {
			t.Fatal("refused plan persisted declarations or failed to finish intake")
		}
	}
	if len(processes) != 2 {
		t.Fatalf("describe/plan processes = %d", len(processes))
	}
	for _, process := range processes {
		if !process.closed || !process.cleaned {
			t.Fatal("planner leaked its process")
		}
	}
}
