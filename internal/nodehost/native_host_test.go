//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type nativeHostState struct {
	OwnerStore
	ClaimStore
}

func (s nativeHostState) ReadSnapshot(ctx context.Context, runID string) (store.RunSnapshot, error) {
	return s.ClaimStore.ReadSnapshot(ctx, runID)
}

func (s nativeHostState) TransitionNode(ctx context.Context, change store.NodeTransition) (store.Applied, error) {
	return s.ClaimStore.TransitionNode(ctx, change)
}

func nativeHostFixture(t *testing.T) (NativeHostConfig, NodeInput, *acquiredClaimStore, ClaimedNodeHost) {
	t.Helper()
	input, state, claimed, _ := claimedNodeFixture(t)
	execution := claimed.Executor.(*nodeExecutor).deps
	execution.Bind = func(context.Context, NodeInput) (ProcessGrant, error) {
		return ProcessGrant{}, errors.New("fixture delegation unavailable")
	}
	authorities, _, _, _, _ := processAuthorityFixture(t)
	t.Cleanup(func() { _ = authorities.Close() })
	config := NativeHostConfig{
		Store: nativeHostState{ClaimStore: state}, Lease: claimed.Lease,
		Source:       source.Options{Engines: claimEngine{resolved: input.Engine}},
		Masks:        NewMaskRegistry(secrets.NewMasker()),
		Router:       &serviceRegistrar{},
		Processes:    &NativeProcesses{Authorities: authorities, Log: func(context.Context, string, string, nodeproto.FrameLog) error { return nil }},
		ValidatePlan: execution.ValidatePlan, CanonicalHash: execution.CanonicalHash,
		Executor: execution,
		Evaluation: EvaluationDependencies{
			Bind: func(context.Context, EvaluationInput) (ProcessGrant, error) {
				return ProcessGrant{}, errors.New("fixture delegation unavailable")
			},
			ReadOutput: func(context.Context, store.NodeRef, int64) (json.RawMessage, error) {
				return json.RawMessage("null"), nil
			},
		},
		Planner: SDKPlanner{Log: func(context.Context, nodeproto.FrameLog) error { return nil }},
		Owner:   OwnerHost{Wait: func(context.Context, store.RunSnapshot) error { return nil }},
	}
	return config, input, state, claimed
}

func TestNativeHostRunsSharedTransportForOwnerAndClaimedWork(t *testing.T) {
	for _, role := range []string{"owner", "claimed"} {
		t.Run(role, func(t *testing.T) {
			config, input, state, _ := nativeHostFixture(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			entry := &source.Entry{Engine: input.Engine, Directory: t.TempDir(), Command: []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "claimed_node"}, SHA: state.run.Spec.Request.Request.Git.SHA, RepoKey: string(state.run.Spec.Request.Request.Git.Repository.Key)}
			authorities, parent, lookup, delegation, services := processAuthorityFixture(t)
			t.Cleanup(func() { _ = authorities.Close() })
			config.Processes.Authorities, config.Processes.URL = authorities, "http://host"
			config.Source.Base = procenv.Base{Captured: []string{"GORACE=atexit_sleep_ms=0"}}
			config.Processes.Session = ProtocolSessionConfig{Environments: sdkTestEnvironment(nil), ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil }}
			delegated := 0
			config.Executor.Bind = func(ctx context.Context, input NodeInput) (ProcessGrant, error) {
				delegated++
				delegation.Binding = capability.Binding{Kind: capability.BindingNode, RunID: input.Accepted.RunID, NodeID: input.NodeID, Holder: input.Authority.Holder, Generation: input.Generation, Attempt: input.Attempt}
				parent.snapshot.Authorities[0].Principal.Capability.Binding = delegation.Binding
				parent.snapshot.Binding.Current = delegation.Binding
				services.RunID, services.Job = input.Accepted.RunID, input.Accepted.Plan.Jobs[0]
				return config.Processes.Delegate(ctx, lookup, delegation, *services)
			}
			owner, claimed, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			if owner.Executor != claimed.Executor {
				t.Fatal("owner and claimed execution use different executors")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if role == "owner" {
				input.Entry = entry
				err = owner.Executor.Execute(ctx, input)
			} else {
				claimed.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return entry, nil }
				err = claimed.Execute(ctx, input)
			}
			if err != nil || delegated != 1 || len(state.changes) != 2 || state.changes[1].Extra.Result.Outcome != store.NodeSuccess {
				t.Fatalf("delegations=%d transitions=%+v error=%v", delegated, state.changes, err)
			}
			authorities.mu.RLock()
			live := len(authorities.live)
			authorities.mu.RUnlock()
			if live != 0 {
				t.Fatal("host returned with a live delegated process")
			}
		})
	}
}

func TestNativeHostDefaultExpansionUsesSharedEvaluator(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, evaluationState, _, _ := evaluationFixture(t)
	state.executorStore = evaluationState
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	state.run.Spec = &spec
	refused := errors.New("evaluation delegation refused")
	config.Evaluation.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) { return ProcessGrant{}, refused }
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	body, failure, err := owner.Expand(t.Context(), input, state.row, StorageBinding{})
	if err != nil || len(body) != 0 || failure == nil || failure.MaskedError != refused.Error() {
		t.Fatalf("body=%s failure=%+v error=%v", body, failure, err)
	}
}

func TestNativeHostRegistersServicesAgainstLiveProcessAuthority(t *testing.T) {
	config, _, _, _ := nativeHostFixture(t)
	authorities, _, parent, delegation, services := processAuthorityFixture(t)
	t.Cleanup(func() { _ = authorities.Close() })
	config.Processes.Authorities, config.Processes.URL = authorities, "http://host"
	if _, _, _, err := NewNativeHost(config); err != nil {
		t.Fatal(err)
	}
	grant, err := config.Processes.Delegate(t.Context(), parent, delegation, *services)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = grant.Revoke() }()
	snapshot, err := authorities.ReadAuthentication(t.Context(), processLookup(capability.Issued{Raw: grant.Binding.Routes.Bearer}), capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	authorized := capability.Authorized{Principal: snapshot.Authorities[0].Principal, Binding: delegation.Binding}
	_, err = callService(t, config.Router.(*serviceRegistrar), authorized, "/node/v1/masks", `{"value":"native-service-secret"}`)
	if err != nil || config.Masks.masker.Mask("native-service-secret") != "***" {
		t.Fatal("registered handler lost the native process service registry:", err)
	}
}
