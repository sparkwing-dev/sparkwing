package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestPlanningDefaults(t *testing.T) {
	for _, secret := range []string{"private-sdk-default", "4242"} {
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

func checkPlanningDefaults(t *testing.T, secret, field string, projects ...[]byte) {
	var project []byte
	if len(projects) > 0 {
		project = projects[0]
	}
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), limits.DefaultLease)
	defer cancel()
	job := nodewire.JobSpec{ID: "safe-job", Steps: []nodewire.StepSpec{{ID: "body"}}}
	switch field {
	case "job":
		job.ID = "job-" + secret
	case "group":
		job.Groups = []string{"group-" + secret}
	case "step":
		job.Steps[0].ID = "step-" + secret
	case "concurrency":
		job.Concurrency = []nodewire.ConcurrencySpec{{Group: "group-" + secret}}
	}
	plan := nodewire.PlanDoc{Jobs: []nodewire.JobSpec{job}}
	f := newStoreOwnerFixture(t, openStore(t), runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "build", Mode: runspec.IntakeExecute,
		Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
		Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}},
	})
	resolved := engine.Resolved{Identity: engineid.Identity{Version: "v1.0.0", ManifestDigest: "manifest", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.0.0"}}}
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
		Masker: masks, Log: func(context.Context, nodewire.FrameLog) error { return nil },
		EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		},
		Start: func(context.Context, PlanningProcessInput, func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
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
		return f.Host.Planner.PrepareCheckout(ctx, CheckoutPlanningInput{Request: row.Request, Entry: entry, Runner: input.GrantedRunner, Project: project, ProjectFile: ".sparkwing/sparkwing.yaml", Planning: f.Host.Planning})
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
		if !errors.As(err, &failure) || failure.Code != hosterr.SecretInIdentifier {
			t.Fatalf("secret identifier was not refused by the shared validator: %v", err)
		}
		if strings.Contains(err.Error()+failure.Message+failure.Hint+fmt.Sprint(failure.Details), secret) {
			t.Fatal("refusal exposed its secret default")
		}
		if snapshot.Run.State != runstate.RunFailed || snapshot.Run.Spec != nil || len(snapshot.Run.Plan) != 0 || len(snapshot.Nodes) != 0 {
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

func TestOwnerRejectsSecretEffectiveLabels(t *testing.T) {
	const secret = "private-sdk-default"
	for _, layer := range []string{"defaults", "pipeline"} {
		for _, label := range []string{"requires", "prefers"} {
			t.Run(layer+"/"+label, func(t *testing.T) {
				config := "defaults: {" + label + ": [pool=" + secret + "]}"
				if layer == "pipeline" {
					config = "pipelines: [{name: build, entrypoint: build, " + label + ": [pool=" + secret + "]}]"
				}
				checkPlanningDefaults(t, secret, "yaml", []byte(config))
			})
		}
	}
	checkPlanningDefaults(t, secret, "safe", []byte("defaults: {prefers: [pool=public]}"))
}

func TestOwnerRejectsLosingDeclarationsContainingSourceCredentials(t *testing.T) {
	const credential = "private-acquisition-credential"
	for _, field := range []string{"requires", "prefers"} {
		t.Run(field, func(t *testing.T) {
			f := newStoreOwnerFixture(t, openStore(t), runspec.Request{
				Version: 1, Team: string(store.DefaultTeam), Pipeline: "build", Mode: runspec.IntakeExecute,
				Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost,
				Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}},
			})
			row, err := f.Tenant.ReadRun(t.Context(), f.Input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			selected := engineid.Identity{Version: "v0.68.0", ManifestDigest: "manifest", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v0.68.0"}}
			masks := secrets.NewMasker()
			f.Host.Masker, f.Host.Masks = masks, NewMaskRegistry(masks)
			f.Host.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
				if err := masks.Register(credential); err != nil {
					t.Fatal(err)
				}
				return &source.Entry{Engine: engine.Resolved{Identity: selected}}, nil
			}
			config := "defaults: {" + field + ": [pool=" + credential + "]}\npipelines: [{name: build, entrypoint: build, " + field + ": [linux]}]"
			declarations, err := settings.ProjectCheckoutDeclarations([]byte(config), ".sparkwing/sparkwing.yaml", "build")
			if err != nil {
				t.Fatal(err)
			}
			f.Host.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
				return OwnerPlanning{
					Declarations: declarations,
					Planning:     PlanningInput{Validation: nodeproto.ValidationContext{Secrets: ownerSealSecrets{}, Placement: nodeproto.PlacementLocal}},
					Plan: func(context.Context) (json.RawMessage, error) {
						return json.RawMessage(`{"jobs":[{"id":"safe-job","steps":[{"id":"body"}]}]}`), nil
					},
					Resolve: func(plan nodewire.PlanDoc) (runspec.Resolved, error) {
						resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: row.Request, Declarations: declarations, Plan: plan})
						return runspec.Resolved{Engine: selected, Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}, err
					},
				}, nil
			}
			accepted := errors.New("unexpected accepted plan")
			f.Host.Project = func(store.RunSnapshot) (store.ScheduleSnapshot, error) { return store.ScheduleSnapshot{}, accepted }
			_, err = f.Host.Run(t.Context(), f.Input)
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != hosterr.SecretInIdentifier {
				t.Fatalf("source credential declaration accepted: %v", err)
			}
			if strings.Contains(err.Error(), credential) {
				t.Fatal("refusal exposed credential")
			}
			snapshot, err := f.Tenant.ReadSnapshot(t.Context(), f.Input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.State != runstate.RunFailed || snapshot.Run.Spec != nil || len(snapshot.Run.Plan) != 0 || len(snapshot.Nodes) != 0 {
				t.Fatal("refused source credential persisted acceptance")
			}
		})
	}
}
