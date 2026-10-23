package nodehost

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestNativeClosureFailures(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := buildNativeSDKFixture(t, ctx, "evalfixture")
	runner := nodeproto.RunnerIdentity{Name: "granted", Type: "local", Labels: []string{}}
	hello, plan := planNativeSDKFixture(t, ctx, bin, "eval", map[string]string{}, nodeproto.RunContext{RunID: "run", Pipeline: "eval"}, runner)
	for _, id := range []string{"source", "skip", "memo", "memo_no_read"} {
		t.Run(id, func(t *testing.T) {
			jobID := strings.TrimSuffix(id, "_no_read")
			var job nodeproto.JobSpec
			for _, declared := range plan.Jobs {
				if declared.ID == jobID {
					job = declared
				}
			}
			if job.ID == "" {
				t.Fatal("fixture job is absent")
			}
			planHash, err := runspec.CanonicalHash(plan)
			if err != nil {
				t.Fatal(err)
			}
			jobHash, err := runspec.CanonicalHash(job)
			if err != nil {
				t.Fatal(err)
			}
			selected := engine.Resolved{Identity: engineid.Identity{Version: "fixture", ManifestDigest: "fixture", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: hello.SDK.Version}}}
			spec := runspec.Spec{Hash: "seal", PlanHash: planHash, Request: runspec.SealedRequest{RunID: "run", Request: runspec.Request{Pipeline: "eval"}}, Resolved: runspec.Resolved{SecretRegistration: runspec.SecretRegistration{Version: 1}, Engine: selected.Identity, Entrypoint: "eval"}}
			spec.Request.Request.Env, err = runspec.EnvironmentDigest(map[string]string{"PATH": os.Getenv("PATH")}, strings.Repeat("5", 64))
			if err != nil {
				t.Fatal(err)
			}
			spec.Resolved.Settings.Options.NoMemo.Value = id == "memo_no_read"
			accepted := AcceptedRun{RunID: "run", Spec: marshalEventFixture(t, spec), SpecHash: string(spec.Hash), Plan: plan, PlanHash: string(planHash)}
			st := newExpansionHostStore(store.RunRow{Execution: runstate.Execution{ID: "run", Spec: &spec, PlanHash: string(planHash), Plan: marshalEventFixture(t, plan)}}, runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(jobID)}, Spec: marshalEventFixture(t, job), SpecHash: string(jobHash), Status: runstate.StatusPending})
			if id == "source" {
				st.node.Status, st.node.Outcome, st.node.Expansion = runstate.StatusDone, runstate.NodeSuccess, runstate.ExpansionExpected
			}
			input := EvaluationInput{Accepted: accepted, NodeID: jobID, SpecHash: string(jobHash), Engine: selected, Entry: &source.Entry{Engine: selected, Command: []string{bin}, Directory: t.TempDir()}, GrantedRunner: runner, RouteBinding: nodeproto.ProcessBinding{RunID: "run", JobID: jobID}}
			revoked, cleaned := false, false
			var mu sync.Mutex
			var logs []string
			masker := secrets.NewMasker()
			masks := NewMaskRegistry(masker)
			cfg := ProtocolSessionConfig{
				Masks:        masks,
				Environments: SDKEnvironment{Store: st, Base: procenv.Base{Local: true, Captured: []string{"PATH=" + os.Getenv("PATH")}}},
				Log: func(_ context.Context, frame nodeproto.FrameLog) error {
					mu.Lock()
					defer mu.Unlock()
					logs = append(logs, frame.Message)
					return nil
				},
				RunCleanups: func(context.Context, CleanupScope) error {
					if !revoked {
						t.Error("evaluation cleanup began with live process authority")
					}
					cleaned = true
					return nil
				},
			}
			host, err := NewOwnerEvaluator(EvaluationDependencies{
				Store: st, Start: cfg.StartEvaluation, Masker: masker, Masks: masks,
				Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: ownerSealSecrets{}},
				EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
					return selected, nil
				},
				ReadOutput: func(context.Context, runspec.NodeRef, int64) (json.RawMessage, error) {
					return json.RawMessage("null"), nil
				},
				Bind: func(_ context.Context, input EvaluationInput) (ProcessGrant, error) {
					binding := input.RouteBinding
					binding.Routes = nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "delegated-process"}
					return ProcessGrant{Binding: binding, Revoke: func() error { revoked = true; return nil }}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			switch jobID {
			case "source":
				body, _, failure, err := host.Expand(ctx, input)
				if err != nil || len(body) != 0 || failure == nil || failure.Code != apierr.SDKError || failure.MaskedError != "generator failed (details withheld: secret declarations unavailable)" {
					t.Fatalf("failed generator became topology: body=%s failure=%+v error=%v", body, failure, err)
				}
			case "skip", "memo":
				input := NodeInput{Accepted: accepted, NodeID: jobID, SpecHash: string(jobHash), Generation: 3, Authority: ClaimAuthority{Holder: "runner", CapabilityPrefix: "claim"}, Engine: selected, Entry: input.Entry, GrantedRunner: runner, RouteBinding: input.RouteBinding}
				st.node.Generation, st.node.ClaimedBy, st.node.CapabilityPrefix = input.Generation, input.Authority.Holder, input.Authority.CapabilityPrefix
				executor, err := NewNodeExecutor(ExecutorDependencies{
					Store: st, Start: cfg.Start, Masker: testMasker(),
					Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: ownerSealSecrets{}},
					EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
						return selected, nil
					},
					Bind: func(_ context.Context, input NodeInput) (ProcessGrant, error) {
						binding := input.RouteBinding
						binding.Routes = nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "delegated-process"}
						return ProcessGrant{Binding: binding, Revoke: func() error { revoked = true; return nil }}, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				host := executor.(*nodeExecutor)
				if jobID == "skip" {
					decision, err := host.skipIf(ctx, input, spec, job)
					if err == nil || decision.Skip || !strings.Contains(err.Error(), "predicate refused") {
						t.Fatalf("failed predicate became a decision: %+v error=%v", decision, err)
					}
				} else {
					key, err := executorPolicyMemoKey(ctx, host, input, spec, job)
					if err == nil || key != "" || !strings.Contains(err.Error(), "memo refused") {
						t.Fatalf("failed memo closure became a miss: key=%q error=%v", key, err)
					}
				}
			}
			if !revoked || !cleaned || st.count != 0 {
				t.Fatalf("closure leaked authority or committed topology: revoked=%v cleaned=%v writes=%d", revoked, cleaned, st.count)
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(strings.Join(logs, "\n"), "body ran") {
				t.Fatal("closure evaluation invoked a job body")
			}
		})
	}
}
