package nodehost

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestClaimedNodeAdmissionCoversSourceAndJoinedExecution(t *testing.T) {
	input, _, host, processes := claimedNodeFixture(t)
	daemon := newFakeDaemon()
	host.Admission = &LocalAdmission{Daemon: daemon, BoxID: testBox}
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
	host.Admission = &LocalAdmission{Daemon: daemon, BoxID: testBox}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host.Admission.Queued = func(context.Context, store.NodeRef, wingwire.Queued) { cancel() }
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
		if change.Cause == store.CauseStart {
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
		admission := LocalAdmission{Daemon: daemon, BoxID: testBox}
		row := localAdmissionRow(first)
		row.Ref.RunID = accepted.RunID
		err := admission.ClaimedNode(t.Context(), accepted, row, func(ctx context.Context) error {
			row.Ref.NodeID = second
			return admission.ClaimedNode(ctx, accepted, row, func(context.Context) error {
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
