package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestCheckoutPlanning(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"merged", "alias", "empty_guard", "local", "controller", "argument", "sdk_default", "origin_head", "sealed_default", "app_default", "default_reject_feature", "default_reject_trunk", "missing_default_when", "missing_default_unless", "missing_location", "refused", "invalid"} {
		t.Run(mode, func(t *testing.T) { checkCheckoutPlanning(t, mode) })
	}
}

func checkCheckoutPlanning(t *testing.T, mode string) {
	t.Helper()
	request := runspec.Request{Pipeline: "build", Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost, ExecutionLocation: "local", RequestedArgs: map[string]string{"explicit": "request"}, Git: runspec.Git{Ref: "trunk", Repository: runspec.Repository{Key: "git:example.com/repo"}}}
	request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 42, Action: "opened", BaseRef: "trunk", BaseSHA: "base-sha", HeadRef: "feature", HeadSHA: "head-sha"}}
	request.Options.Priority = runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 7}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "priority"}}
	when, unless := "[]", "[]"
	wantPassed := true
	switch mode {
	case "empty_guard":
	case "local":
		when = "[where:local]"
	case "controller":
		request.ExecutionLocation, request.Origin = "controller", runspec.OriginTrigger
		when = "[where:controller]"
	case "argument":
		when = "[arg:explicit=request, arg:inherited=pipeline, arg:default=defaults]"
	case "sdk_default":
		when = "[arg:sdk=provided-by-sdk]"
	case "origin_head", "sealed_default", "app_default":
		when = "[git:branch=default]"
	case "default_reject_feature":
		unless, request.Git.Ref = "[git:branch=default]", "feature"
	case "default_reject_trunk":
		unless, wantPassed = "[git:branch=default]", false
	case "missing_default_when":
		when, wantPassed = "[git:branch=default]", false
	case "missing_default_unless":
		unless, wantPassed = "[git:branch=default]", false
	case "refused":
		unless, wantPassed = "[arg:sdk=provided-by-sdk]", false
	case "missing_location":
		when, request.ExecutionLocation, wantPassed = "[where:local]", "", false
	case "invalid":
		when = "[profile:local]"
	}
	entrypoint := "build"
	if mode == "alias" {
		request.Pipeline, entrypoint = "deploy-prod", "Deploy"
	}
	project := []byte("defaults:\n  args: {explicit: defaults, inherited: defaults, default: defaults}\n  guards: {when: [where:controller]}\npipelines:\n  - name: " + request.Pipeline + "\n    entrypoint: " + entrypoint + "\n    args: {explicit: pipeline, inherited: pipeline}\n    guards: {when: " + when + ", unless: " + unless + "}\n    requires: [pool=build]\n")
	identity := engine.Identity{Version: "v1.0.0", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
	entry := &source.Entry{Engine: engine.Resolved{Identity: identity}, DefaultBranch: "trunk"}
	if strings.HasPrefix(mode, "missing_default") {
		entry.DefaultBranch = ""
	}
	if mode == "app_default" {
		request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DefaultBranch: "trunk"}}
		request.Git.DefaultBranch = runspec.Optional[string]{Set: true, Value: "trunk"}
		entry.DefaultBranch = "changed-origin-head"
	}
	if mode == "sealed_default" {
		request.Git.DefaultBranch = runspec.Optional[string]{Set: true, Value: "trunk"}
		entry.DefaultBranch = "changed-origin-head"
	}
	unit := store.OwnerLeaseRef{Kind: store.OwnerTrigger, Name: "trigger", EvalFence: runspec.EvalFence{RunID: "run", Holder: "host", Generation: 1}}
	sealed := runspec.SealedRequest{RunID: "run", Request: request, Hash: "request"}
	s := newOwnerSealStore(store.RunRow{ID: "run", State: store.RunPending, Request: sealed, Owner: &unit})
	var processes []*scriptedSDK
	defaultArg, secretArg := "provided-by-sdk", "private-default"
	planner := SDKPlanner{
		Masker: secrets.NewMasker(), Log: func(context.Context, nodeproto.FrameLog) error { return nil },
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return entry.Engine, nil
		},
		CanonicalHash: func(any) (runspec.Hash, error) { return ownerPlanHash, nil },
		ValidatePlan: func(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
			var plan nodeproto.PlanDoc
			err := json.Unmarshal(raw, &plan)
			return plan, err
		},
		Start: func(context.Context, PlanningProcessInput, func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
			process := newCheckoutSDK(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}, nodeproto.PipelineDescription{Name: entrypoint, Extra: true, Args: []nodeproto.ArgumentDescription{{Name: "sdk", Type: "string", Default: &defaultArg}, {Name: "secret", Type: "string", Secret: true, Default: &secretArg}}, Secrets: []nodeproto.SecretDeclaration{{Name: "REQUIRED"}, {Name: "OPTIONAL", Optional: true}}})
			processes = append(processes, process)
			return process, nil
		},
	}
	host := OwnerHost{
		Masker: planner.Masker, Store: s, Renew: s.RenewOwner, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour},
		Acquire: func(context.Context, RunInput) (*source.Entry, error) { return entry, nil },
		Prepare: func(ctx context.Context, _ RunInput, e *source.Entry) (OwnerPlanning, error) {
			return planner.PrepareCheckout(ctx, CheckoutPlanningInput{Request: sealed, Entry: e, Runner: nodeproto.RunnerIdentity{Name: "granted", Type: "local"}, Project: project, ProjectFile: "sparkwing.yaml"})
		},
		Wait: func(context.Context, store.RunSnapshot) error { return errors.New("unexpected graph wait") },
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := host.Run(t.Context(), RunInput{RunID: "run", RequestHash: "request", SealedRequest: raw, Owner: store.Actor{Kind: store.ActorRunHost, Owner: unit}})
	if mode == "invalid" {
		if err == nil || s.row.Spec != nil || len(processes) != 0 {
			t.Fatal("invalid guard reached SDK startup or acceptance")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	wantOutcome := "success"
	if !wantPassed {
		wantOutcome = "failed"
	}
	if completion.Outcome != wantOutcome || s.row.Spec == nil || s.row.Spec.Resolved.Guards.Passed != wantPassed {
		t.Fatalf("completion=%+v spec=%+v", completion, s.row.Spec)
	}
	resolved := s.row.Spec.Resolved
	if resolved.Args["explicit"] != "request" || resolved.Args["inherited"] != "pipeline" || resolved.Args["default"] != "defaults" || resolved.Args["sdk"] != defaultArg || resolved.Args["secret"] != secretArg || !reflect.DeepEqual(resolved.SecretNames, []string{"REQUIRED", "OPTIONAL"}) || !reflect.DeepEqual(resolved.SecretArgs, []string{"secret"}) || resolved.Settings.Options.Priority.Value != 7 {
		t.Fatalf("lost resolved inputs: %+v", resolved)
	}
	if !reflect.DeepEqual(s.row.Spec.Request, sealed) || resolved.Engine != identity || !reflect.DeepEqual(resolved.Env, request.Env) {
		t.Fatal("checkout preparation changed the intake or engine identity")
	}
	if wantPassed {
		if len(processes) != 2 || processes[1].requests[len(processes[1].requests)-1].Args["sdk"] != defaultArg || processes[1].requests[len(processes[1].requests)-1].Run.Git.DefaultBranch != resolved.DefaultBranch.Value || processes[1].requests[len(processes[1].requests)-1].Run.RunID != "run" || processes[1].requests[len(processes[1].requests)-1].Run.Pipeline != request.Pipeline || processes[1].requests[len(processes[1].requests)-1].Pipeline != entrypoint || resolved.Entrypoint != entrypoint {
			t.Fatal("planner did not receive resolved arguments and run facts")
		}
		wantPR := &nodeproto.PullRequest{Number: 42, Action: "opened", BaseRef: "trunk", BaseSHA: "base-sha", HeadRef: "feature", HeadSHA: "head-sha"}
		if !reflect.DeepEqual(processes[1].requests[len(processes[1].requests)-1].Run.Trigger.PullRequest, wantPR) {
			t.Fatal("planning discarded sealed pull-request facts")
		}
	} else if len(processes) != 1 || resolved.Guards.Reason == "" {
		t.Fatal("guard refusal planned SDK code or omitted refusal evidence")
	}
	if mode == "app_default" && resolved.DefaultBranchSource != runspec.DefaultBranchApp || mode == "origin_head" && (resolved.DefaultBranchSource != runspec.DefaultBranchOriginHEAD || sealed.Request.Git.App.Set) {
		t.Fatal("default-branch discovery changed App provenance")
	}
	for _, process := range processes {
		if !process.closed || !process.cleaned {
			t.Fatal("checkout planning leaked an SDK process")
		}
	}
}

func checkCheckoutGuardMask(t *testing.T, value string) {
	t.Helper()
	secret := "credential:" + strings.ToValidUTF8(value, "界") + ":end"
	project, err := json.Marshal(map[string]any{"defaults": map[string]any{"guards": map[string]any{"unless": []string{"arg:token=" + secret}}}})
	if err != nil {
		t.Fatal(err)
	}
	resolved := engine.Resolved{Identity: engine.Identity{Version: "v1.0.0", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}}
	masker := secrets.NewMasker()
	planner := SDKPlanner{
		Masker: masker, Log: func(context.Context, nodeproto.FrameLog) error { return nil },
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		},
		Start: func(context.Context, PlanningProcessInput, func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
			return newCheckoutSDK(nodeproto.PlanDoc{}, nodeproto.PipelineDescription{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Type: "string", Default: &secret, Secret: true}}}), nil
		},
	}
	prepared, err := planner.PrepareCheckout(t.Context(), CheckoutPlanningInput{Request: runspec.SealedRequest{RunID: "run", Request: runspec.Request{Pipeline: "build", ExecutionLocation: "local"}}, Entry: &source.Entry{Engine: resolved}, Project: project, ProjectFile: "sparkwing.yaml"})
	if err != nil {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("project validation error contains a secret")
		}
		return
	}
	reason := prepared.Resolved.Guards.Reason
	if prepared.Resolved.Guards.Passed || reason != "run guard unless[0] refused execution" || strings.Contains(reason, secret) {
		t.Fatal("guard diagnostics contain a secret or omit the refusal")
	}
	encoded, err := json.Marshal(reason)
	if err != nil || strings.Contains(string(encoded), secret) {
		t.Fatal("serialized guard diagnostic contains a secret")
	}
}
