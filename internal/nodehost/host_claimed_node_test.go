package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type claimExecutor func(context.Context, NodeInput) error

func (f claimExecutor) Execute(ctx context.Context, input NodeInput) error {
	return f(ctx, input)
}

func TestClaimedNodeLifecycle(t *testing.T) {
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
				row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending, ClaimedBy: "runner", CapabilityPrefix: "claim", Generation: 2, Attempt: 4, SpecHash: "job-spec"}
				s := newClaimedHostStore(store.RunSnapshot{Run: store.RunRow{ID: "run", Spec: &spec, PlanHash: ownerPlanHash, Plan: body}, Nodes: []store.NodeRow{row}}, mode)
				input, err := NewNodeInput(AcceptedRun{RunID: "run", Spec: raw, SpecHash: "spec", PlanHash: ownerPlanHash, Plan: plan}, row, engine.Resolved{Identity: identity}, nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{}})
				if err != nil {
					t.Fatal(err)
				}
				acquired, executed, joined := false, false, false
				host := ClaimedNodeHost{
					Store: s, Lease: lease.Keeper{Interval: 10 * time.Second, Lease: time.Minute}, Masker: testMasker("private"),
					Acquire: func(_ context.Context, projected source.Spec, _ source.Options) (*source.Entry, error) {
						acquired = true
						if s.beats.Load() != 1 || projected.SHA != request.Request.Git.SHA || projected.Engine == nil || *projected.Engine != identity {
							t.Error("source ran without the exact accepted award")
						}
						return &source.Entry{RepoKey: projected.RepoKey, SHA: projected.SHA, Engine: engine.Resolved{Identity: identity}}, nil
					},
					Executor: claimExecutor(func(ctx context.Context, in NodeInput) error {
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
					if !errors.As(err, &cancelled) || cancelled.Reason != "operator requested stop" || acquired || len(s.reports) != 1 || s.reports[0].To != store.NodeCancelled {
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

func checkClaimedAwardFence(t *testing.T, field uint8, value string) {
	t.Helper()
	var trace []string
	var processes []*scriptedSDK
	input, storage, host := executionHostFixture(t, "success", &trace, &processes)
	foreign := "foreign:" + value
	switch field % 8 {
	case 0:
		input.Authority.Holder = foreign
	case 1:
		input.Authority.CapabilityPrefix = foreign
	case 2:
		input.Generation++
	case 3:
		input.Attempt++
	case 4:
		input.SpecHash = foreign
	case 5:
		input.Accepted.RunID = foreign
	case 6:
		input.NodeID = foreign
	case 7:
		input.Execution.NoProgressTimeout.Value++
	}
	acquired := false
	host.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) {
		acquired = true
		return &source.Entry{}, nil
	}
	if err := host.Execute(t.Context(), input); err == nil || acquired || len(processes) != 0 || len(storage.reports) != 0 || storage.beats.Load() != 0 {
		t.Fatal("changed award crossed the claim fence")
	}
}
