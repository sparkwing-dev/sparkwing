package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type planningSDK struct {
	expansionSDK
	secret string
}

func (p *planningSDK) Request(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
	if request.Op == nodeproto.OpHello && request.Binding != nil {
		return nodeproto.Reply{}, errors.New("planner received job route authority")
	}
	if request.Op == nodeproto.OpDescribe {
		raw, err := json.Marshal(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Secret: true, Default: &p.secret}}}}})
		return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
	}
	if request.Op == nodeproto.OpPlan && request.Args["token"] != p.secret {
		return nodeproto.Reply{}, errors.New("planner lost its secret default")
	}
	return p.expansionSDK.Request(ctx, request)
}

// CheckPlanningDefaults proves that SDK defaults reach the shared validator's
// matcher, including short secrets. The validator and store remain fake boundaries;
// native placement refusal and transactional persistence are separate cells.
func CheckPlanningDefaults(t *testing.T) {
	t.Helper()
	for _, secret := range []string{"private-sdk-default", "42"} {
		for _, field := range []string{"job", "group", "safe"} {
			t.Run(secret+"/"+field, func(t *testing.T) {
				identity := engine.Identity{Version: "v1.0.0", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
				resolved := engine.Resolved{Identity: identity}
				job := nodeproto.JobSpec{ID: "safe-job"}
				if field == "job" {
					job.ID = "job-" + secret
				}
				if field == "group" {
					job.Groups = []string{"group-" + secret}
				}
				var processes []*planningSDK
				p := nodehost.SDKPlanner{
					Masker: secrets.NewMasker(), Log: func(context.Context, nodeproto.FrameLog) error { return nil },
					EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
						return resolved, nil
					},
					Start: func(context.Context, nodehost.PlanningProcessInput, func(context.Context, nodeproto.FrameLog) error) (nodehost.ExecutorProcess, error) {
						process := &planningSDK{expansionSDK: expansionSDK{plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}}, secret: secret}
						processes = append(processes, process)
						return process, nil
					},
					CanonicalHash: func(any) (runspec.Hash, error) { return ownerPlanHash, nil },
					ValidatePlan: func(raw json.RawMessage, validation nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
						var plan nodeproto.PlanDoc
						if err := json.Unmarshal(raw, &plan); err != nil {
							return plan, err
						}
						for _, job := range plan.Jobs {
							for _, id := range append([]string{job.ID}, job.Groups...) {
								if validation.Secrets.ContainsSecret(id) {
									return nodeproto.PlanDoc{}, &apierr.Error{Code: apierr.PlanInvalid, Message: "identifier contains a secret"}
								}
							}
						}
						return plan, nil
					},
				}
				prepared, err := p.Prepare(t.Context(), nodehost.PlanningProcessInput{Entry: &source.Entry{Engine: resolved}, Run: nodeproto.RunContext{RunID: "run", Pipeline: "build"}}, nodehost.PlanningInput{})
				if err != nil {
					t.Fatal(err)
				}
				if prepared.Args["token"] != secret || len(prepared.SecretArgs) != 1 || prepared.SecretArgs[0] != "token" {
					t.Fatal("SDK default or classification missing from resolved inputs")
				}
				s, err := acceptPlanningDefaults(t, prepared, resolved)
				if field == "safe" {
					if err != nil || s.row.Spec == nil {
						t.Fatalf("safe plan was not accepted: %v", err)
					}
					if s.row.Spec.Resolved.Args["token"] != secret || len(s.row.Spec.Resolved.SecretArgs) != 1 {
						t.Fatal("accepted spec lost defaults or secret classification")
					}
				} else {
					var failure *apierr.Error
					if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid || strings.Contains(err.Error(), secret) || s.row.Spec != nil || len(s.row.Plan) != 0 {
						t.Fatalf("refused plan was accepted or leaked its value: %v", err)
					}
				}
				for _, process := range processes {
					if !process.closed || !process.cleaned {
						t.Fatal("planner leaked its process")
					}
				}
			})
		}
	}
}

func acceptPlanningDefaults(t *testing.T, prepared nodehost.PreparedPlanning, engine engine.Resolved) (*ownerSealStore, error) {
	t.Helper()
	owner := store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Name: "trigger", Holder: "owner", Generation: 1}
	request := runspec.Request{Pipeline: "build"}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	s := &ownerSealStore{row: store.RunRow{ID: "run", State: store.RunPending, Request: runspec.SealedRequest{Request: request, Hash: "request"}, Owner: &owner}}
	accepted := errors.New("stop at accepted plan")
	host := nodehost.OwnerHost{
		Mask:  func(value string) string { return value },
		Store: s, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour},
		Acquire: func(context.Context, nodehost.RunInput) (*source.Entry, error) {
			return &source.Entry{Engine: engine}, nil
		},
		Prepare: func(context.Context, nodehost.RunInput, *source.Entry) (nodehost.OwnerPlanning, error) {
			return nodehost.OwnerPlanning{Resolved: runspec.Resolved{Engine: engine.Identity, Args: prepared.Args, SecretArgs: prepared.SecretArgs}, Planning: prepared.Input, Functions: prepared.Functions}, nil
		},
		Project: func(store.RunSnapshot) (store.ScheduleSnapshot, error) { return store.ScheduleSnapshot{}, accepted },
		Wait:    func(context.Context, store.RunSnapshot) error { return errors.New("unexpected wait before planning") },
	}
	_, err = host.Run(t.Context(), nodehost.RunInput{RunID: "run", SealedRequest: raw, RequestHash: "request", Owner: store.Actor{Kind: store.ActorRunHost, Owner: owner}})
	if errors.Is(err, accepted) {
		err = nil
	}
	return s, err
}
