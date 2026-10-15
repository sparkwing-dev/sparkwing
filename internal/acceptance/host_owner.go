package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const ownerPlanHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type ownerSealStore struct {
	nodehost.OwnerStore
	row        store.RunRow
	trace      []string
	refused    error
	renewError error
	beat       store.LeaseBeat
	finished   store.FinishRunRequest
}

type ownerSealSecrets struct{}

func (ownerSealSecrets) ContainsSecret(string) bool { return false }

func (s *ownerSealStore) RenewOwner(context.Context, store.Actor) (store.LeaseBeat, error) {
	return s.beat, s.renewError
}

func (s *ownerSealStore) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return store.RunSnapshot{Run: s.row}, nil
}

func (s *ownerSealStore) AcceptPlan(_ context.Context, input store.PlanAcceptance) (store.AcceptedPlan, error) {
	s.trace = append(s.trace, "accept")
	if s.refused != nil {
		return store.AcceptedPlan{}, s.refused
	}
	if !reflect.DeepEqual(input.Seal.Request, s.row.Request) {
		return store.AcceptedPlan{}, errors.New("host replaced intake")
	}
	s.row.Plan, s.row.PlanHash = input.Seal.Plan, ownerPlanHash
	s.row.Spec = &runspec.Spec{Declarations: input.Seal.Declarations, Request: input.Seal.Request, Hash: "spec", PlanHash: ownerPlanHash, Resolved: input.Seal.Resolved}
	return store.AcceptedPlan{Spec: *s.row.Spec, Plan: s.row.Plan, PlanHash: s.row.PlanHash}, nil
}

func (s *ownerSealStore) TransitionRun(_ context.Context, input store.RunTransition) (store.Applied, error) {
	if s.row.Spec == nil {
		return store.Applied{}, errors.New("started before acceptance")
	}
	s.trace = append(s.trace, "start")
	s.row.State = input.To
	return store.Applied{}, nil
}

func (s *ownerSealStore) FinishRunAndUnit(_ context.Context, input store.FinishRunRequest) (store.Applied, error) {
	s.trace = append(s.trace, "finish")
	s.finished = input
	return store.Applied{}, nil
}

// CheckOwnerSeals exercises the common owner's two-seal boundary with a fake
// store. Store atomicity and native placement drivers remain separate cells.
func CheckOwnerSeals(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"new", "resume", "refused", "setup_failure", "rejected_owner", "cancelled_owner", "secret_setup_failure", "secret_accept_failure", "cancelled_acquire_success", "lost_acquire_success"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			owner := store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Name: "trigger", Holder: "host", Generation: 3}
			request := runspec.Request{Pipeline: "build", PullRequest: runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 42}}}
			sealed, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			s := &ownerSealStore{row: store.RunRow{ID: "run", State: store.RunPending, Request: runspec.SealedRequest{Request: request, Hash: "request"}, Owner: &owner}}
			if mode == "resume" {
				s.row.Spec = &runspec.Spec{Request: s.row.Request, Hash: "spec", PlanHash: ownerPlanHash}
				s.row.Plan, s.row.PlanHash = json.RawMessage(`{"jobs":[]}`), ownerPlanHash
			}
			refused := errors.New("acceptance refused")
			secret := "private-owner-credential"
			secretFailure := &apierr.Error{Code: apierr.Unavailable, Message: secret, Hint: "retry " + secret, Details: map[string]any{"credential": secret}}
			if mode == "refused" {
				s.refused = refused
			}
			if mode == "rejected_owner" {
				s.renewError = &apierr.Error{Code: apierr.LockHeld}
			}
			if mode == "cancelled_owner" {
				s.beat = store.LeaseBeat{CancelRequested: true, CancelReason: "operator requested stop"}
			}
			if mode == "secret_accept_failure" {
				s.refused = secretFailure
			}
			host := nodehost.OwnerHost{
				Mask:  func(value string) string { return strings.ReplaceAll(value, secret, "***") },
				Store: s, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour},
				Acquire: func(_ context.Context, in nodehost.RunInput) (*source.Entry, error) {
					s.trace = append(s.trace, "acquire")
					if mode == "resume" && in.Accepted == nil {
						t.Error("resume did not pass persisted acceptance to acquisition")
					}
					if mode == "setup_failure" {
						return nil, errors.New("source unavailable")
					}
					if mode == "secret_setup_failure" {
						return nil, secretFailure
					}
					if mode == "cancelled_acquire_success" {
						cancel(&lease.CancelError{Reason: "cancel_requested"})
					}
					if mode == "lost_acquire_success" {
						cancel(lease.ErrLeaseLost)
					}
					return &source.Entry{}, nil
				},
				Prepare: func(context.Context, nodehost.RunInput, *source.Entry) (nodehost.OwnerPlanning, error) {
					s.trace = append(s.trace, "prepare")
					return nodehost.OwnerPlanning{Planning: nodehost.PlanningInput{Validation: nodeproto.ValidationContext{Secrets: ownerSealSecrets{}}}, Functions: nodehost.PlanningFunctions{
						Plan: func(context.Context) (json.RawMessage, error) {
							s.trace = append(s.trace, "plan")
							return json.RawMessage(`{"jobs":[]}`), nil
						},
						Validate: func(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
							var plan nodeproto.PlanDoc
							err := json.Unmarshal(raw, &plan)
							return plan, err
						},
						Hash: func(any) (runspec.Hash, error) { return ownerPlanHash, nil },
					}}, nil
				},
				Project: func(store.RunSnapshot) (store.ScheduleSnapshot, error) { return store.ScheduleSnapshot{}, nil },
				Decide:  func(store.ScheduleSnapshot) store.ScheduleDecision { return store.ScheduleDecision{Outcome: "success"} },
				Wait: func(context.Context, store.RunSnapshot) error {
					t.Error("empty graph waited")
					return errors.New("unexpected wait")
				},
			}
			result, err := host.Run(ctx, nodehost.RunInput{RunID: "run", SealedRequest: sealed, RequestHash: "request", Owner: store.Actor{Kind: store.ActorRunHost, Owner: owner}})
			want := []string{"acquire", "prepare", "plan", "accept", "start", "finish"}
			switch mode {
			case "resume":
				want = []string{"acquire", "start", "finish"}
			case "refused", "secret_accept_failure":
				want = []string{"acquire", "prepare", "plan", "accept"}
				if mode == "refused" && !errors.Is(err, refused) {
					t.Fatalf("error = %v", err)
				}
			case "setup_failure", "secret_setup_failure":
				want = []string{"acquire", "finish"}
				if err == nil || s.finished.Transition.To != store.RunFailed {
					t.Fatalf("finish = %+v error = %v", s.finished, err)
				}
			case "rejected_owner":
				want = nil
				if !errors.Is(err, lease.ErrLeaseLost) {
					t.Fatalf("error = %v", err)
				}
			case "cancelled_owner":
				want = nil
				var cancelled *lease.CancelError
				if !errors.As(err, &cancelled) || cancelled.Reason != "operator requested stop" {
					t.Fatalf("owner cancellation lost its reason: %v", err)
				}
			case "lost_acquire_success":
				want = []string{"acquire"}
				if !errors.Is(err, lease.ErrLeaseLost) || s.finished.Transition.RunID != "" {
					t.Fatalf("lost owner finished: %+v error=%v", s.finished, err)
				}
			case "cancelled_acquire_success":
				want = []string{"acquire", "finish"}
				var cancelled *lease.CancelError
				if !errors.As(err, &cancelled) || s.finished.Transition.To != store.RunCancelled || s.finished.Unit != owner {
					t.Fatalf("unaccepted cancellation left its owner unfinished: %+v error=%v", s.finished, err)
				}
			}
			if strings.HasPrefix(mode, "secret_") {
				var rendered *apierr.Error
				if !errors.Is(err, secretFailure) || !errors.As(err, &rendered) || rendered.Code != secretFailure.Code || strings.Contains(err.Error()+rendered.Message+rendered.Hint, secret) || rendered.Details["credential"] != "***" {
					t.Fatalf("owner error lost its cause or exposed credentials: %v", err)
				}
			}
			if !reflect.DeepEqual(s.trace, want) {
				t.Fatalf("trace = %v, want %v", s.trace, want)
			}
			if mode == "new" || mode == "resume" {
				if err != nil || result.Outcome != "success" || s.finished.Unit != owner {
					t.Fatalf("completion = %+v finish = %+v error = %v", result, s.finished, err)
				}
			}
		})
	}
}
