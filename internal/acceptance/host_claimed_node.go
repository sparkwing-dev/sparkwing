package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type claimedHostStore struct {
	nodehost.ClaimStore
	snapshot store.RunSnapshot
	beats    atomic.Int64
	mode     string
	reports  []store.NodeTransition
}

func (s *claimedHostStore) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return s.snapshot, nil
}

func (s *claimedHostStore) ReadNode(context.Context, store.NodeRef) (store.NodeRow, error) {
	return s.snapshot.Nodes[0], nil
}

func (s *claimedHostStore) TransitionNode(_ context.Context, transition store.NodeTransition) (store.Applied, error) {
	s.reports = append(s.reports, transition)
	return store.Applied{}, nil
}

func (s *claimedHostStore) RenewNodeClaim(_ context.Context, ref store.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	row := s.snapshot.Nodes[0]
	if ref != row.Ref || actor.Kind != store.ActorHolder || actor.Holder != row.ClaimedBy || actor.CapabilityPrefix != row.CapabilityPrefix || actor.Generation != row.Generation {
		return store.LeaseBeat{}, errors.New("renewal changed claim authority")
	}
	beats := s.beats.Add(1)
	switch {
	case s.mode == "rejected" || s.mode == "lost_during_work" && beats > 1:
		return store.LeaseBeat{}, &apierr.Error{Code: apierr.LockHeld}
	case s.mode == "cancelled":
		return store.LeaseBeat{CancelRequested: true}, nil
	case s.mode == "silent":
		return store.LeaseBeat{}, errors.New("unavailable")
	default:
		return store.LeaseBeat{}, nil
	}
}

type claimExecutor func(context.Context, nodehost.NodeInput) error

func (f claimExecutor) Execute(ctx context.Context, input nodehost.NodeInput) error {
	return f(ctx, input)
}

// CheckClaimedNodeLifecycle proves source gating and joined execution through
// the common claim adapter. Store and executor implementations are contract fakes.
func CheckClaimedNodeLifecycle(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "rejected", "cancelled", "silent", "lost_during_work"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				identity := engine.Identity{Version: "v1.0.0", ManifestDigest: "verified", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
				plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}}
				request := runspec.SealedRequest{RunID: "run", Request: runspec.Request{Pipeline: "build"}}
				request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://example.com/acme/repo.git", PipelineDir: ".sparkwing"}
				request.Request.Git.SHA, request.Request.Git.Repository.Key = strings.Repeat("a", 40), "example.com/acme/repo"
				spec := runspec.Spec{Hash: "spec", PlanHash: ownerPlanHash, Request: request, Resolved: runspec.Resolved{Engine: identity}}
				raw, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				row := store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending, ClaimedBy: "runner", CapabilityPrefix: "claim", Generation: 2, Attempt: 4, SpecHash: "job-spec"}
				s := &claimedHostStore{mode: mode, snapshot: store.RunSnapshot{Run: store.RunRow{ID: "run", Spec: &spec, PlanHash: ownerPlanHash, Plan: body}, Nodes: []store.NodeRow{row}}}
				input := nodehost.NodeInput{Accepted: nodehost.AcceptedRun{RunID: "run", Spec: raw, SpecHash: "spec", PlanHash: ownerPlanHash, Plan: plan}, NodeID: "job", Generation: 2, Attempt: 4, SpecHash: "job-spec", Engine: engine.Resolved{Identity: identity}, Authority: nodehost.ClaimAuthority{Holder: "runner", CapabilityPrefix: "claim"}}
				acquired, executed, joined := false, false, false
				host := nodehost.ClaimedNodeHost{
					Store: s, Lease: lease.Keeper{Interval: 10 * time.Second, Lease: time.Minute}, Mask: func(string) string { return "masked" },
					Acquire: func(_ context.Context, projected source.Spec, _ source.Options) (*source.Entry, error) {
						acquired = true
						if s.beats.Load() != 1 || projected.SHA != request.Request.Git.SHA || projected.Engine == nil || *projected.Engine != identity {
							t.Error("source ran without the exact accepted award")
						}
						return &source.Entry{RepoKey: projected.RepoKey, SHA: projected.SHA, Engine: engine.Resolved{Identity: identity}}, nil
					},
					Executor: claimExecutor(func(ctx context.Context, in nodehost.NodeInput) error {
						executed = true
						defer func() { joined = true }()
						if in.Entry == nil || in.Generation != 2 || in.Attempt != 4 {
							t.Error("executor lost accepted inputs")
						}
						if mode == "lost_during_work" {
							<-ctx.Done()
						}
						return nil
					}),
				}
				err = host.Execute(t.Context(), input)
				switch mode {
				case "success":
					if err != nil || !acquired || !executed || !joined {
						t.Fatalf("success: acquired=%t executed=%t joined=%t error=%v", acquired, executed, joined, err)
					}
				case "lost_during_work":
					if !errors.Is(err, lease.ErrLeaseLost) || !joined || len(s.reports) != 0 {
						t.Fatalf("lost work: joined=%t reports=%+v error=%v", joined, s.reports, err)
					}
				case "cancelled":
					var cancelled *lease.CancelError
					if !errors.As(err, &cancelled) || acquired || len(s.reports) != 1 || s.reports[0].To != store.NodeCancelled {
						t.Fatalf("cancellation: acquired=%t reports=%+v error=%v", acquired, s.reports, err)
					}
				default:
					want := lease.ErrLeaseLost
					if mode == "silent" {
						want = lease.ErrSilent
					}
					if !errors.Is(err, want) || acquired || executed || len(s.reports) != 0 {
						t.Fatalf("lost setup: acquired=%t executed=%t reports=%+v error=%v", acquired, executed, s.reports, err)
					}
				}
				beats := s.beats.Load()
				<-time.After(time.Minute)
				if s.beats.Load() != beats {
					t.Fatal("renewal outlived claim host")
				}
			})
		})
	}
}
