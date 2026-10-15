package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type acquiredClaimStore struct {
	*executorStore
	run   store.RunRow
	renew func(context.Context) (store.LeaseBeat, error)
}

type refusedClaimSnapshot struct {
	ClaimStore
	failure error
}

func (s refusedClaimSnapshot) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return store.RunSnapshot{}, s.failure
}

func TestClaimedNodeHostMasksReadFailuresBeforeRenewal(t *testing.T) {
	input, state, host, processes := claimedNodeFixture(t)
	secret := "private-claim-read-credential"
	failure := &apierr.Error{Code: apierr.Unavailable, Message: secret, Hint: secret, Details: map[string]any{"credential": secret}}
	host.Store = refusedClaimSnapshot{ClaimStore: state, failure: failure}
	host.Mask = func(value string) string { return strings.ReplaceAll(value, secret, "***") }
	state.renew = func(context.Context) (store.LeaseBeat, error) {
		t.Error("renewed after the acceptance read failed")
		return store.LeaseBeat{}, nil
	}
	err := host.Execute(t.Context(), input)
	var rendered *apierr.Error
	if !errors.Is(err, failure) || !errors.As(err, &rendered) || strings.Contains(err.Error()+rendered.Message+rendered.Hint, secret) || rendered.Details != nil {
		t.Fatalf("claim read exposed credentials or lost its cause: %v", err)
	}
	if len(*processes) != 0 || len(state.changes) != 0 {
		t.Fatal("failed acceptance read started execution or reported a result")
	}
}

func (s *acquiredClaimStore) RenewNodeClaim(ctx context.Context, ref store.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	if ref != s.row.Ref || actor.Kind != store.ActorHolder || actor.Holder != s.row.ClaimedBy || actor.Generation != s.row.Generation || actor.CapabilityPrefix != s.row.CapabilityPrefix {
		return store.LeaseBeat{}, errors.New("renewal changed the award")
	}
	return s.renew(ctx)
}

func (s *acquiredClaimStore) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return store.RunSnapshot{Run: s.run, Nodes: []store.NodeRow{s.row}}, nil
}

func claimedNodeFixture(t *testing.T) (NodeInput, *acquiredClaimStore, ClaimedNodeHost, *[]*executorProcess) {
	t.Helper()
	input, st, deps, processes := executorFixture(t)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://example.com/acme/repo.git", PipelineDir: ".sparkwing"}
	spec.Request.Request.Git.SHA = strings.Repeat("a", 40)
	spec.Request.Request.Git.Repository.Key = "example.com/acme/repo"
	input.Accepted.Spec, _ = json.Marshal(spec)
	plan, _ := json.Marshal(input.Accepted.Plan)
	s := &acquiredClaimStore{executorStore: st, run: store.RunRow{ID: input.Accepted.RunID, Spec: &spec, Plan: plan, PlanHash: input.Accepted.PlanHash}}
	s.renew = func(context.Context) (store.LeaseBeat, error) { return store.LeaseBeat{}, nil }
	deps.Store = s
	executor, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	entry := input.Entry
	entry.SHA, entry.RepoKey = spec.Request.Request.Git.SHA, string(spec.Request.Request.Git.Repository.Key)
	input.Entry = nil
	host := ClaimedNodeHost{
		Store: s, Executor: executor, Lease: lease.Keeper{Interval: 10 * time.Second, Lease: time.Minute}, Mask: deps.Mask,
		Acquire: func(_ context.Context, in source.Spec, _ source.Options) (*source.Entry, error) {
			if in.Engine == nil || *in.Engine != spec.Resolved.Engine || in.SHA != spec.Request.Request.Git.SHA || in.RepoKey != entry.RepoKey {
				t.Fatal("acquisition lost the accepted source or engine")
			}
			return entry, nil
		},
	}
	return input, s, host, processes
}

func TestClaimedNodeHostRenewsBeforeSourceAndJoinsExecution(t *testing.T) {
	input, s, host, processes := claimedNodeFixture(t)
	var trace []string
	s.renew = func(context.Context) (store.LeaseBeat, error) {
		trace = append(trace, "renew")
		return store.LeaseBeat{}, nil
	}
	acquire := host.Acquire
	host.Acquire = func(ctx context.Context, spec source.Spec, opts source.Options) (*source.Entry, error) {
		trace = append(trace, "source")
		return acquire(ctx, spec, opts)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace, []string{"renew", "source"}) {
		t.Fatalf("trace = %v", trace)
	}
	if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
		t.Fatal("execution outlived host")
	}
	if len(s.changes) != 2 || s.changes[1].Extra.Result.Outcome != store.NodeSuccess {
		t.Fatalf("transitions = %+v", s.changes)
	}
}

func TestClaimedNodeHostRejectsLostAuthorityBeforeAcquisition(t *testing.T) {
	for _, code := range []apierr.Code{apierr.LockHeld, apierr.Unauthenticated, apierr.Forbidden} {
		t.Run(string(code), func(t *testing.T) {
			input, s, host, processes := claimedNodeFixture(t)
			s.renew = func(context.Context) (store.LeaseBeat, error) { return store.LeaseBeat{}, &apierr.Error{Code: code} }
			host.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) {
				t.Fatal("revoked claim acquired source")
				return nil, nil
			}
			if err := host.Execute(t.Context(), input); !errors.Is(err, lease.ErrLeaseLost) {
				t.Fatalf("error = %v", err)
			}
			if len(*processes) != 0 || len(s.changes) != 0 {
				t.Fatal("revoked claim executed or reported")
			}
		})
	}
}

func TestClaimedNodeHostDoesNotRenewAChangedAward(t *testing.T) {
	for _, change := range []string{"holder", "generation", "attempt", "prefix", "seal", "settings", "engine", "missing_job_hash", "job_hash"} {
		t.Run(change, func(t *testing.T) {
			input, s, host, _ := claimedNodeFixture(t)
			switch change {
			case "holder":
				input.Authority.Holder = "other"
			case "generation":
				input.Generation++
			case "attempt":
				input.Attempt++
			case "prefix":
				input.Authority.CapabilityPrefix = "other"
			case "seal":
				input.Accepted.SpecHash = "other"
			case "settings":
				input.Execution.Retry.Value++
			case "engine":
				input.Engine.Identity.Version = "other"
			case "missing_job_hash":
				input.SpecHash = ""
			case "job_hash":
				input.SpecHash = "other"
			}
			s.renew = func(context.Context) (store.LeaseBeat, error) {
				t.Fatal("changed award renewed")
				return store.LeaseBeat{}, nil
			}
			if err := host.Execute(t.Context(), input); err == nil {
				t.Fatal("accepted changed award")
			}
			if len(s.changes) != 0 {
				t.Fatal("changed award reported a result")
			}
		})
	}
}

func TestClaimedNodeHostReportsSetupFailuresThroughTheClaim(t *testing.T) {
	for _, mode := range []string{"acquire", "empty", "engine", "sha", "repository", "refused_report"} {
		t.Run(mode, func(t *testing.T) {
			input, s, host, processes := claimedNodeFixture(t)
			acquire := host.Acquire
			host.Acquire = func(ctx context.Context, spec source.Spec, opts source.Options) (*source.Entry, error) {
				entry, err := acquire(ctx, spec, opts)
				switch mode {
				case "acquire", "refused_report":
					return entry, errors.New("private-source-token")
				case "empty":
					return nil, nil
				case "engine":
					entry.Engine.Identity.Version = "different"
				case "sha":
					entry.SHA = "different"
				case "repository":
					entry.RepoKey = "different"
				}
				return entry, err
			}
			if mode == "refused_report" {
				s.fail = store.CauseReport
			}
			err := host.Execute(t.Context(), input)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("error = %v", err)
			}
			if len(*processes) != 0 || len(s.changes) != 1 {
				t.Fatalf("processes=%d changes=%+v", len(*processes), s.changes)
			}
			change := s.changes[0]
			if change.Actor.Holder != input.Authority.Holder || change.Actor.Generation != input.Generation || change.Extra.Result.Outcome != store.NodeFailed || strings.Contains(change.Extra.Result.MaskedError, "private") {
				t.Fatalf("report = %+v", change)
			}
		})
	}
}

func TestClaimedNodeHostLossDuringAcquisitionDiscardsEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, s, host, processes := claimedNodeFixture(t)
		beats := 0
		s.renew = func(context.Context) (store.LeaseBeat, error) {
			beats++
			if beats == 1 {
				return store.LeaseBeat{}, nil
			}
			return store.LeaseBeat{}, &apierr.Error{Code: apierr.LockHeld}
		}
		acquire := host.Acquire
		host.Acquire = func(ctx context.Context, spec source.Spec, opts source.Options) (*source.Entry, error) {
			<-ctx.Done()
			return acquire(context.Background(), spec, opts)
		}
		if err := host.Execute(t.Context(), input); !errors.Is(err, lease.ErrLeaseLost) {
			t.Fatalf("error = %v", err)
		}
		if len(s.changes) != 0 || len(*processes) != 0 || beats != 2 {
			t.Fatalf("changes=%+v processes=%d beats=%d", s.changes, len(*processes), beats)
		}
	})
}
