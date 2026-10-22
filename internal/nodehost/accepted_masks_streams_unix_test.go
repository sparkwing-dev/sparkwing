//go:build unix

package nodehost

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestSealedSecretsStayMaskedInNativeSDKStreams(t *testing.T) {
	property := func(seed [24]byte) bool {
		value := "sealed-" + hex.EncodeToString(seed[:])
		for _, placement := range []string{"fresh", "resumed", "claimed"} {
			t.Run(placement, func(t *testing.T) {
				masks := NewMaskRegistry(secrets.NewMasker())
				var mu sync.Mutex
				var logs []nodeproto.FrameLog
				sink := func(_ context.Context, frame nodeproto.FrameLog) error {
					mu.Lock()
					defer mu.Unlock()
					logs = append(logs, frame)
					return nil
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				command := []string{exe, "-test.run=^TestSealedMaskStreamChild$", "--", value}
				var failure string
				if placement == "fresh" {
					state, host, input := newOwnerFixture(t, false)
					host.Masks, host.Masker = masks, masks.masker
					request := &state.snapshot.Run.Request.Request
					request.RequestedArgs = map[string]string{"token": value}
					request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
					input.SealedRequest, _ = json.Marshal(request)
					planner, planning, fixture, _ := sdkPlanningFixture(t)
					planner.Masks, planner.Masker, planner.Log = masks, masks.masker, sink
					planning.Args, planning.SecretArgs = request.RequestedArgs, []string{"token"}
					planning.Entry.Command = command
					planning.Runner.Labels = []string{}
					cfg := ProtocolSessionConfig{Environments: sdkTestEnvironment(nil), ParseFrame: protocolTestCodec}
					planner.Start = cfg.StartPlanning
					host.Acquire = func(ctx context.Context, _ RunInput) (*source.Entry, error) {
						prepared, err := planner.Prepare(ctx, planning, fixture.input)
						if err == nil {
							_, err = PlanInitial(ctx, prepared.Input, prepared.Plan)
						}
						return nil, err
					}
					_, err = host.Run(t.Context(), input)
					if err == nil {
						t.Fatal("SDK planning failure disappeared")
					}
					failure = err.Error()
				} else {
					config, input, state, _ := nativeHostFixture(t)
					config.Masks = masks
					config.Source.Base = procenv.Base{Captured: []string{"GORACE=atexit_sleep_ms=0"}}
					spec := state.run.Spec
					spec.Request.Request.RequestedArgs = nil
					spec.Request.Request.SecretArgs = runspec.Optional[[]string]{}
					spec.Resolved.Args = map[string]string{"default_token": value}
					spec.Resolved.SecretArgs = []string{"default_token"}
					spec.Resolved.SecretRegistration, _ = runspec.BuildSecretRegistration(spec.Request.Request, spec.Resolved)
					input.Accepted.Spec, _ = json.Marshal(spec)
					persistNativeAcceptance(t, state, input.Accepted)
					authorities, parent, lookup, delegation, services := processAuthorityFixture(t)
					t.Cleanup(func() { _ = authorities.Close() })
					config.Processes.Authorities, config.Processes.URL = authorities, "http://host"
					config.Processes.Log = func(ctx context.Context, _, _ string, frame nodeproto.FrameLog) error { return sink(ctx, frame) }
					config.Processes.Session = ProtocolSessionConfig{Environments: sdkTestEnvironment(state), ParseFrame: protocolTestCodec}
					config.Executor.Bind = func(ctx context.Context, in NodeInput) (ProcessGrant, error) {
						delegation.Binding = capability.Binding{Kind: capability.BindingNode, RunID: in.Accepted.RunID, NodeID: in.NodeID, Holder: in.Authority.Holder, Generation: int64(in.Generation), Attempt: int64(in.Attempt)}
						parent.snapshot.Authorities[0].Principal.Capability.Binding = delegation.Binding
						parent.snapshot.Binding.Current = delegation.Binding
						services.RunID, services.Job = in.Accepted.RunID, in.Accepted.Plan.Jobs[0]
						return config.Processes.Delegate(ctx, lookup, delegation, *services)
					}
					_, claimed, _, err := NewNativeHost(config)
					if err != nil {
						t.Fatal(err)
					}
					entry := &source.Entry{Engine: input.Engine, Command: command, SHA: spec.Request.Request.Git.SHA, RepoKey: string(spec.Request.Request.Git.Repository.Key)}
					if placement == "claimed" {
						claimed.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return entry, nil }
						err = claimed.Execute(t.Context(), input)
						encoded, _ := json.Marshal(state.changes)
						failure = string(encoded)
						if err != nil {
							failure += err.Error()
						}
						if len(state.changes) != 2 || state.changes[1].Extra.Result == nil || state.changes[1].Extra.Result.Outcome != "failed" {
							t.Fatalf("SDK body did not fail: %s", failure)
						}
					} else {
						ownerState, host, run := newOwnerFixture(t, true)
						host.Masks, host.Masker = masks, masks.masker
						ownerState.snapshot.Run.Spec = spec
						ownerState.snapshot.Run.PlanHash = string(spec.PlanHash)
						ownerState.snapshot.Run.Plan, _ = json.Marshal(input.Accepted.Plan)
						accepted := input.Accepted
						run.Accepted = &accepted
						host.Acquire = func(ctx context.Context, _ RunInput) (*source.Entry, error) {
							grant, err := config.Executor.Bind(ctx, input)
							if err != nil {
								return nil, err
							}
							input.Entry, input.RouteBinding, input.processRevoke = entry, grant.Binding, grant.Revoke
							process, err := config.Processes.Start(ctx, input, ExecutorActivity{})
							if err != nil {
								return nil, err
							}
							defer func() { _ = process.Close(); _ = process.Cleanup(context.WithoutCancel(ctx)) }()
							session := &executorSession{process: process}
							executor := claimed.Executor.(*nodeExecutor)
							if err = executor.hello(ctx, session, input); err != nil {
								return nil, err
							}
							reply, err := process.Request(ctx, nodeproto.Request{ID: "body", Op: nodeproto.OpRunStep, Job: input.NodeID, Step: "body", Attempt: int(input.Attempt), Settled: []string{}})
							if err != nil {
								return nil, err
							}
							var result nodeproto.RunResult
							if err = reply.Decode(&result); err != nil {
								return nil, err
							}
							return nil, result.Error
						}
						_, err = host.Run(t.Context(), run)
						if err == nil {
							t.Fatal("resumed SDK failure disappeared")
						}
						failure = err.Error()
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if strings.Contains(failure, value) {
					t.Fatal("sealed secret escaped SDK failure")
				}
				for _, frame := range logs {
					if strings.Contains(frame.Message+string(frame.Attrs), value) {
						t.Fatal("sealed secret escaped SDK stream")
					}
				}
				for _, prefix := range []string{"stdout:", "stderr:"} {
					if !slices.ContainsFunc(logs, func(frame nodeproto.FrameLog) bool {
						return strings.HasPrefix(frame.Message, prefix) && strings.Contains(frame.Message, "***")
					}) {
						t.Fatalf("missing masked %s stream: %+v; failure %s", prefix, logs, failure)
					}
				}
				if !strings.Contains(failure, "***") {
					t.Fatalf("SDK failure was not observed: %s", failure)
				}
			})
		}
		return !t.Failed()
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 4}); err != nil {
		t.Fatal(err)
	}
}

func TestSealedMaskStreamChild(t *testing.T) {
	index := slices.Index(os.Args, "--")
	if index < 0 {
		return
	}
	value := os.Args[index+1]
	requests, frames := os.NewFile(3, "requests"), os.NewFile(4, "frames")
	defer requests.Close()
	defer frames.Close()
	writer := json.NewEncoder(frames)
	scanner := bufio.NewScanner(requests)
	for scanner.Scan() {
		var request nodeproto.Request
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		reply := nodeproto.Reply{ID: request.ID, OK: true}
		var result any
		switch request.Op {
		case nodeproto.OpHello:
			result = nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: request.Engine.Version}, Features: []string{}}
		case nodeproto.OpDescribe:
			result = nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "build", Args: []nodeproto.ArgumentDescription{}}}}
		case nodeproto.OpPlan:
			if request.Run != nil && request.Run.Pipeline == "build" {
				reply.OK = false
				reply.Error = &apierr.Error{Code: apierr.SDKError, Message: value}
			} else {
				result = nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
			}
		case nodeproto.OpRunStep:
			result = nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: value}}
		default:
			os.Exit(3)
		}
		if request.Op == nodeproto.OpPlan || request.Op == nodeproto.OpRunStep {
			fmt.Fprintln(os.Stdout, "stdout:"+value)
			fmt.Fprintln(os.Stderr, "stderr:"+value)
		}
		if reply.OK {
			reply.Result, _ = json.Marshal(result)
		}
		if writer.Encode(reply) != nil {
			os.Exit(4)
		}
	}
	os.Exit(0)
}
