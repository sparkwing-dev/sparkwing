package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestOwnerHostGuardRefusalBypassesPlanAndCache(t *testing.T) {
	f, host, input := newOwnerFixture(t, false)
	planning := newPlanningFixture(t)
	input.PlanCache = planning.cache
	planning.cache.onLookup = func() { t.Error("refused guard read plan cache") }
	planning.cache.onPut = func() { t.Error("refused guard wrote plan cache") }
	host.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
		return OwnerPlanning{Resolved: runspec.Resolved{Guards: runspec.Guards{Unless: []string{"where:local"}, Reason: "refused"}}, Planning: planning.input, Functions: PlanningFunctions{
			Plan: func(context.Context) (json.RawMessage, error) {
				t.Error("refused guard invoked SDK plan")
				return nil, nil
			},
			Validate: func(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
				var plan nodeproto.PlanDoc
				err := json.Unmarshal(raw, &plan)
				if plan.Jobs == nil || len(plan.Jobs) != 0 {
					t.Error("guard refusal proposed executable work")
				}
				return plan, err
			},
			Hash: func(any) (runspec.Hash, error) { return runspec.Hash(strings.Repeat("a", 64)), nil },
		}}, nil
	}
	if _, err := host.Run(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if f.accepted.Seal.Resolved.Guards.Reason != "refused" || f.accepted.RunID != "run" {
		t.Fatal("guard refusal did not pass through acceptance")
	}
}

func TestSDKCheckoutPlanningCacheIncludesProject(t *testing.T) {
	var prior runspec.Hash
	for _, requires := range []string{"pool=build", "pool=test"} {
		planner, process, fixture, _ := sdkPlanningFixture(t)
		planner.CanonicalHash = planningTestHash
		input := CheckoutPlanningInput{Request: runspec.SealedRequest{RunID: "run", Request: runspec.Request{Pipeline: "build", Team: "team", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:123"}}, Source: runspec.Source{Entrypoint: "build"}}}, Entry: process.Entry, Planning: fixture.input, ProjectFile: "sparkwing.yaml", Project: []byte("defaults:\n  requires: [" + requires + "]\n")}
		prepared, err := planner.PrepareCheckout(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		digest := prepared.Planning.Inputs.ProjectDigest
		if !planCacheDigestValid(digest) || digest == prior || prepared.Planning.Inputs.Team != "team" || prepared.Planning.Inputs.Repository != "gh:123" || prepared.Planning.Inputs.Entrypoint != "build" {
			t.Fatal("checkout planning reused unrelated project identity or scope")
		}
		prior = digest
	}
}

func TestCheckoutProjectDiscoveryStopsAtAcquiredRoot(t *testing.T) {
	parent := t.TempDir()
	checkout := filepath.Join(parent, "checkout")
	nested := filepath.Join(checkout, "services", "api", ".sparkwing")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(file, content string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(parent, "sparkwing.yaml"), "outside checkout")
	if raw, file, err := readCheckoutProject(t.Context(), checkout, nested); err != nil || raw != nil || file != "" {
		t.Fatalf("read an ancestor outside checkout: %q %q %v", raw, file, err)
	}
	write(filepath.Join(checkout, "sparkwing.yaml"), "root")
	if raw, file, err := readCheckoutProject(t.Context(), checkout, nested); err != nil || string(raw) != "root" || file != "sparkwing.yaml" {
		t.Fatalf("root project: %q %q %v", raw, file, err)
	}
	write(filepath.Join(checkout, "services", "sparkwing.yaml"), "nearest")
	if raw, file, err := readCheckoutProject(t.Context(), checkout, nested); err != nil || string(raw) != "nearest" || file != "services/sparkwing.yaml" {
		t.Fatalf("nested project: %q %q %v", raw, file, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := readCheckoutProject(ctx, checkout, nested); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery: %v", err)
	}
}

func TestOwnerHostResolvesValidatedPlansBeforeAcceptance(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "planned", true: "cached"}[cached], func(t *testing.T) {
			f, host, input := newOwnerFixture(t, false)
			planning := newPlanningFixture(t)
			functions := planning.functions()
			functions.Hash = func(any) (runspec.Hash, error) { return runspec.Hash(strings.Repeat("a", 64)), nil }
			if cached {
				if _, err := PlanInitial(t.Context(), planning.input, functions); err != nil {
					t.Fatal(err)
				}
			}
			input.PlanCache = planning.cache
			request := &f.snapshot.Run.Request.Request
			request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
			request.RequestedArgs = map[string]string{"token": "private"}
			input.SealedRequest, _ = json.Marshal(request)
			resolved := false
			host.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
				return OwnerPlanning{Planning: planning.input, Functions: functions, Resolve: func(plan nodeproto.PlanDoc) (runspec.Resolved, error) {
					if !reflect.DeepEqual(plan, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "test"}}}) {
						t.Fatal("resolver did not receive the validated plan")
					}
					resolved = true
					return runspec.Resolved{Args: request.RequestedArgs}, nil
				}}, nil
			}
			if _, err := host.Run(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if !resolved || !reflect.DeepEqual(f.accepted.Seal.Resolved.SecretArgs, []string{"token"}) {
				t.Fatal("acceptance omitted resolved inputs or intake classifications")
			}
		})
	}
}

func TestOwnerHostResolutionFailureNeverAccepts(t *testing.T) {
	f, host, input := newOwnerFixture(t, false)
	prepare := host.Prepare
	failure := errors.New("settings exceed ceiling")
	host.Prepare = func(ctx context.Context, in RunInput, entry *source.Entry) (OwnerPlanning, error) {
		prepared, err := prepare(ctx, in, entry)
		prepared.Resolve = func(nodeproto.PlanDoc) (runspec.Resolved, error) { return runspec.Resolved{}, failure }
		return prepared, err
	}
	if _, err := host.Run(t.Context(), input); !errors.Is(err, failure) || f.accepted.RunID != "" || f.finished.Transition.RunID != "run" {
		t.Fatalf("resolution error=%v accept=%+v finish=%+v", err, f.accepted, f.finished)
	}
}
