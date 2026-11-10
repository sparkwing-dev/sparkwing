package nodehost

import (
	"context"
	"errors"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestQueuedClaimCancellationBeforeHeartbeatReportsCancelled(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancelled", false: "live"}[cancelled], func(t *testing.T) {
			input, state, host, processes := claimedNodeFixture(t)
			input.Attempt, state.row.Attempt = 0, 0
			polls := 0
			host.Admission = ComposeStartGate(&capacityProbe{admit: func(context.Context) (runstate.AcquireSlotResponse, error) {
				polls++
				if polls == 1 {
					return runstate.AcquireSlotResponse{Kind: runstate.AcquireQueued}, nil
				}
				if cancelled {
					now := time.Now()
					state.run.CancelRequestedAt, state.row.CancelRequestedAt = &now, &now
				}
				return runstate.AcquireSlotResponse{}, &apierr.Error{Code: hosterr.Unauthenticated, Message: "credential is not live"}
			}}, nil, false)
			err := host.Execute(t.Context(), input)
			if polls != 2 || len(*processes) != 0 || len(state.changes) != 1 || state.row.Attempt != 0 {
				t.Fatalf("polls=%d processes=%d changes=%+v attempt=%d error=%v", polls, len(*processes), state.changes, state.row.Attempt, err)
			}
			result := state.changes[0].Extra.Result
			if !cancelled {
				if result.Outcome != runstate.NodeFailed || result.Reason != string(hosterr.Unauthenticated) {
					t.Fatalf("live authentication failure was hidden: %+v error=%v", result, err)
				}
				return
			}
			var cause *lease.CancelError
			if result.Outcome != runstate.NodeCancelled || result.Reason != string(hosterr.ReasonCancelled) || result.Failure != nil || !errors.As(err, &cause) {
				t.Fatalf("queued cancellation became an authentication failure: %+v error=%v", result, err)
			}
		})
	}
}

func TestClaimedNodeAdmissionCoversSourceAndJoinedExecution(t *testing.T) {
	input, _, host, processes := claimedNodeFixture(t)
	daemon := newFakeDaemon()
	host.Admission = ComposeStartGate(&capacityProbe{admit: func(context.Context) (runstate.AcquireSlotResponse, error) {
		return runstate.AcquireSlotResponse{Kind: runstate.AcquireGranted}, nil
	}}, &LocalAdmission{Daemon: daemon}, false)
	acquire := host.Acquire
	host.Acquire = func(ctx context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
		if live, _, _ := daemon.counts(); live != 1 {
			t.Fatalf("source preparation escaped machine admission: live=%d", live)
		}
		return acquire(ctx, spec, options)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 1 || released != 1 {
		t.Fatalf("claimed host leaked admission: live=%d granted=%d released=%d", live, granted, released)
	}
	if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
		t.Fatal("admission returned before process cleanup")
	}
}

func TestClaimedNodeCancellationWhileQueuedNeverAcquiresSource(t *testing.T) {
	input, state, host, processes := claimedNodeFixture(t)
	daemon := newFakeDaemon()
	daemon.hostGate = make(chan struct{})
	host.Admission = ComposeStartGate(&capacityProbe{admit: func(context.Context) (runstate.AcquireSlotResponse, error) {
		return runstate.AcquireSlotResponse{Kind: runstate.AcquireGranted}, nil
	}}, &LocalAdmission{Daemon: daemon}, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host.Admission.compute.Queued = func(context.Context, runspec.NodeRef, wingwire.Queued) { cancel() }
	host.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) {
		t.Error("acquired source before admission")
		return nil, errors.New("unadmitted source acquisition")
	}
	if err := host.Execute(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation lost its cause: %v", err)
	}
	if live, _, _ := daemon.counts(); live != 0 || len(*processes) != 0 {
		t.Fatal("cancelled claim retained machine resources or started execution")
	}
	for _, change := range state.changes {
		if change.Cause == runstate.CauseStart {
			t.Fatal("queued claim started an execution attempt")
		}
	}
}

func FuzzClaimedNodeMachineLeasesKeepSeparateJobs(f *testing.F) {
	f.Add("build", "deploy")
	f.Add("a:b", "a/b")
	f.Fuzz(func(t *testing.T, first, second string) {
		if first == "" || second == "" || first == second {
			return
		}
		accepted := localAdmissionSeal(t, localAdmissionOptions())
		daemon := newFakeDaemon()
		admission := LocalAdmission{Daemon: daemon}
		row := localAdmissionRow(first)
		row.Ref.RunID = runspec.RunID(accepted.RunID)
		err := admission.node(t.Context(), accepted, row, func(ctx context.Context) error {
			row.Ref.NodeID = runspec.NodeID(second)
			return admission.node(ctx, accepted, row, func(context.Context) error {
				requests := daemon.requests()
				if live, _, _ := daemon.counts(); live != 2 || len(requests) != 2 || requests[0].RunID == requests[1].RunID || requests[0].SubLease || requests[1].SubLease {
					t.Fatalf("separate claimed jobs shared machine ownership: %+v live=%d", requests, live)
				}
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if live, granted, released := daemon.counts(); live != 0 || granted != 2 || released != 2 {
			t.Fatalf("job ownership leaked: live=%d granted=%d released=%d", live, granted, released)
		}
	})
}
