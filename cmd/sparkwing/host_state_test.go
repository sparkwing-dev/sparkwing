package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type hostStateController struct {
	controllerwire.Controller
	plan   func(context.Context, string, controllerwire.PlanSubmission) (controllerwire.PlanAccepted, error)
	step   func(context.Context, string, string, controllerwire.StepReportRequest) error
	finish func(context.Context, string, controllerwire.FinishRequest) (controllerwire.Applied, error)
	renew  func(context.Context, string, controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error)
}

type nodeControllerState struct {
	nodehost.PrivateHostState
	node func(context.Context, controllerwire.NodeRef) (controllerwire.Controller, error)
}

func (s nodeControllerState) NodeController(ctx context.Context, ref controllerwire.NodeRef) (controllerwire.Controller, error) {
	return s.node(ctx, ref)
}

func TestCLIHostStepUsesIndependentNodeAuthorityProperty(t *testing.T) {
	property := func(run, node, step string, attempt uint16, refuse bool) bool {
		root := hostStateController{step: func(context.Context, string, string, controllerwire.StepReportRequest) error {
			t.Fatal("step used the run owner's bearer")
			return nil
		}}
		called := false
		failure := errors.New("node authority unavailable")
		nodes := nodeControllerState{PrivateHostState: &cliOwnerStore{}, node: func(_ context.Context, ref controllerwire.NodeRef) (controllerwire.Controller, error) {
			if ref != (controllerwire.NodeRef{RunID: runspec.RunID(run), NodeID: runspec.NodeID(node)}) {
				t.Fatal("step selected another node's authority")
			}
			if refuse {
				return nil, failure
			}
			return hostStateController{step: func(_ context.Context, gotRun, gotNode string, body controllerwire.StepReportRequest) error {
				called = gotRun == run && gotNode == node && body.StepID == step && body.SourceAttempt == int64(attempt)+1
				return nil
			}}, nil
		}}
		state, err := nodehost.NewRemoteHostStore(root, nodes, "unit", func() bool { return false })
		if err != nil {
			return false
		}
		err = state.RecordStep(t.Context(), store.StepReport{
			Attempt: runspec.AttemptIdentity{RunID: run, NodeID: node, Attempt: int64(attempt) + 1}, StepID: step, State: "passed",
		})
		return refuse && errors.Is(err, failure) && !called || !refuse && err == nil && called
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func (c hostStateController) AcceptPlan(ctx context.Context, run string, body controllerwire.PlanSubmission) (controllerwire.PlanAccepted, error) {
	return c.plan(ctx, run, body)
}

func (c hostStateController) RecordStep(ctx context.Context, run, job string, body controllerwire.StepReportRequest) error {
	return c.step(ctx, run, job, body)
}

func (c hostStateController) Finish(ctx context.Context, run string, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
	return c.finish(ctx, run, body)
}

func (c hostStateController) Renew(ctx context.Context, unit string, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	return c.renew(ctx, unit, body)
}

func TestCLIHostStatePreservesStepAttemptProperty(t *testing.T) {
	property := func(run, job, step, message string, attempt, generation uint16) bool {
		report := store.StepReport{
			Attempt: runspec.AttemptIdentity{RunID: run, NodeID: job, Attempt: int64(attempt) + 1, Generation: int64(generation) + 1},
			StepID:  step, State: "failed", MaskedError: message,
		}
		called := false
		failure := errors.New("step not committed")
		client := hostStateController{step: func(_ context.Context, gotRun, gotJob string, body controllerwire.StepReportRequest) error {
			called = gotRun == run && gotJob == job && body.StepID == step && body.State == report.State && body.SourceAttempt == report.Attempt.Attempt && body.MaskedError == message
			return failure
		}}
		state, err := nodehost.NewRemoteHostStore(client, &cliOwnerStore{}, "awarded-unit", func() bool { return false })
		return err == nil && errors.Is(state.RecordStep(t.Context(), report), failure) && called
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestCLIHostStatePreservesPlanAndRenewal(t *testing.T) {
	request := store.PlanAcceptance{
		RunID: "current-run", Actor: store.Actor{Owner: store.OwnerLeaseRef{EvalFence: runspec.EvalFence{RunID: "current-run", Holder: "owner", Generation: 7}}},
		Seal: runspec.SealInput{Plan: json.RawMessage(`{"jobs":[]}`)},
	}
	want := controllerwire.PlanAccepted{Plan: request.Seal.Plan, PlanHash: "plan-hash", Replayed: true}
	beat := controllerwire.RenewalResponse{ExpiresAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), CancelRequested: true, CancelReason: "cancelled by user", Watched: true, WatchedJobs: []string{"build"}}
	client := hostStateController{
		plan: func(_ context.Context, run string, body controllerwire.PlanSubmission) (controllerwire.PlanAccepted, error) {
			if run != request.RunID || !reflect.DeepEqual(body.Plan, request.Seal.Plan) {
				t.Fatal("acceptance changed the current run or declarations")
			}
			return want, nil
		},
		renew: func(_ context.Context, unit string, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
			if unit != "awarded-unit" || body.Executing {
				t.Fatal("renewal changed the award or invented compute")
			}
			return beat, nil
		},
	}
	state, err := nodehost.NewRemoteHostStore(client, &cliOwnerStore{}, "awarded-unit", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := state.AcceptPlan(t.Context(), request)
	if err != nil || accepted.PlanHash != string(want.PlanHash) || !accepted.Replayed || !reflect.DeepEqual(accepted.Plan, want.Plan) || accepted.Owner != nil {
		t.Fatalf("accepted = %+v, %v", accepted, err)
	}
	for _, renew := range []func() (store.LeaseBeat, error){
		func() (store.LeaseBeat, error) { return state.RenewOwner(t.Context(), request.Actor, false) },
		func() (store.LeaseBeat, error) {
			return state.RenewNodeClaim(t.Context(), controllerwire.NodeRef{}, request.Actor)
		},
	} {
		got, err := renew()
		if err != nil || got.ExpiresAt != beat.ExpiresAt || got.CancelReason != beat.CancelReason || !got.CancelRequested || !got.Watched || !reflect.DeepEqual(got.WatchedNodes, beat.WatchedJobs) {
			t.Fatalf("renewal = %+v, %v", got, err)
		}
	}
}

func TestCLIHostStateRejectsIncompleteComposition(t *testing.T) {
	for _, input := range []struct {
		controller controllerwire.Controller
		private    nodehost.PrivateHostState
		unit       string
	}{
		{nil, &cliOwnerStore{}, "unit"},
		{hostStateController{}, nil, "unit"},
		{hostStateController{}, &cliOwnerStore{}, ""},
	} {
		if state, err := nodehost.NewRemoteHostStore(input.controller, input.private, input.unit, func() bool { return false }); err == nil || state != nil {
			t.Fatalf("incomplete composition = %+v, %v", state, err)
		}
	}
	var _ nodehost.NativeHostStore = (*nodehost.RemoteHostStore)(nil)
}

func TestCLIHostStateHTTPDoesNotSendActorAuthorityProperty(t *testing.T) {
	property := func(secret string, ordinal, generation uint16, executing bool) bool {
		attempt := int64(ordinal) + 1
		actor := store.Actor{
			Kind: store.ActorRunHost, CapabilityPrefix: secret,
			Owner: store.OwnerLeaseRef{EvalFence: runspec.EvalFence{RunID: "run", Holder: secret, Generation: runspec.ClaimGeneration(int64(generation) + 1)}},
		}
		calls := 0
		wire, err := client.NewEngineClient(client.EngineOptions{
			URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "private-bearer", nil },
			HTTP: &http.Client{Transport: profileTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer private-bearer" {
					t.Fatal("host client lost its private awarded bearer")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				var want map[string]any
				var response any
				switch r.URL.Path {
				case "/api/v1/runs/run/jobs/build/steps/finish":
					want = map[string]any{"step_id": "test", "state": "passed", "source_attempt": float64(attempt)}
					response = controllerwire.StepRecorded{Recorded: true}
				case "/api/v1/runs/run/finish":
					want = map[string]any{}
					response = controllerwire.Applied{Replayed: true, StateSeq: 12, Generation: int64(generation) + 1, Attempt: attempt}
				case "/api/v1/claims/awarded-unit/heartbeat":
					want = map[string]any{"executing": executing}
					response = controllerwire.RenewalResponse{WatchedJobs: []string{}}
				default:
					t.Fatal("unexpected host route:", r.URL.Path)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("host write = %#v, want %#v", got, want)
				}
				calls++
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: http.Header{}}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		state, err := nodehost.NewRemoteHostStore(wire, &cliOwnerStore{}, "awarded-unit", func() bool { return !executing })
		if err != nil {
			t.Fatal(err)
		}
		err = state.RecordStep(t.Context(), store.StepReport{
			Actor: actor, Attempt: runspec.AttemptIdentity{RunID: "run", NodeID: "build", Attempt: attempt, Generation: int64(generation) + 1},
			StepID: "test", State: "passed",
		})
		if err != nil {
			return false
		}
		applied, err := state.FinishRunAndUnit(t.Context(), store.FinishRunRequest{
			Unit: actor.Owner, Transition: store.RunCompletion{RunID: "run", Actor: actor, Cause: store.RunFinish},
		})
		if err != nil || !applied.Replayed || applied.StateSeq != 12 || int64(applied.Generation) != int64(generation)+1 || int64(applied.Attempt) != attempt {
			return false
		}
		_, err = state.RenewOwner(t.Context(), actor, executing)
		return err == nil && calls == 3
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestCLIHostStateFinishRefusesInvalidOwnerOrVerdict(t *testing.T) {
	owner := store.OwnerLeaseRef{EvalFence: runspec.EvalFence{RunID: "run", Holder: "host", Generation: 1}}
	want := errors.New("completion not committed")
	calls := 0
	wire := hostStateController{finish: func(_ context.Context, run string, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
		calls++
		if run != "run" || body.Reason != "plan_failed" || body.Error == nil || body.Error.Message != "safe failure" {
			t.Fatalf("finish = %s %+v", run, body)
		}
		return controllerwire.Applied{StateSeq: 10}, want
	}}
	state, err := nodehost.NewRemoteHostStore(wire, &cliOwnerStore{}, "awarded-unit", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	valid := store.FinishRunRequest{Unit: owner, Transition: store.RunCompletion{
		RunID: "run", Actor: store.Actor{Owner: owner}, Cause: store.RunFinish, Reason: "plan_failed", MaskedError: "safe failure",
	}}
	for _, mutate := range []func(*store.FinishRunRequest){
		func(r *store.FinishRunRequest) { r.Unit.Generation++ },
		func(r *store.FinishRunRequest) { r.Transition.Cause = store.RunPublish },
	} {
		request := valid
		mutate(&request)
		if _, err := state.FinishRunAndUnit(t.Context(), request); err == nil || calls != 0 {
			t.Fatal("invalid finish reached the controller")
		}
	}
	if applied, err := state.FinishRunAndUnit(t.Context(), valid); !errors.Is(err, want) || applied != (store.Applied{}) || calls != 1 {
		t.Fatalf("uncommitted completion = %+v, %v, calls=%d", applied, err, calls)
	}
}

func TestCLIHostStateFinishesRefusedAdmissionWithItsOwnerUnit(t *testing.T) {
	state, err := nodehost.NewRemoteHostStore(hostStateController{}, &cliOwnerStore{}, "unit", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	owner := store.OwnerLeaseRef{Kind: store.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "run", Holder: "host", Generation: 1}}
	request := store.FinishRunRequest{Unit: owner, Transition: store.RunCompletion{
		RunID: "run", Actor: store.Actor{Owner: owner}, Cause: store.RunPolicyRefused, OwnerPolicy: &store.OwnerPolicyResult{Kind: store.PolicyAdmissionFail}, Reason: "resources_refused",
	}}
	state.Finish = func(_ context.Context, got store.FinishRunRequest) (store.Applied, error) {
		if !reflect.DeepEqual(got, request) {
			t.Fatal("admission refusal changed its policy cause or owner unit")
		}
		return store.Applied{StateSeq: 3}, nil
	}
	applied, err := state.FinishRunAndUnit(t.Context(), request)
	if err != nil || applied.StateSeq != 3 {
		t.Fatalf("admission completion = %+v, %v", applied, err)
	}
}
