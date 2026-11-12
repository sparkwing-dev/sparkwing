package nodehost

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestLateDiagnosticCannotStealDetachedSegment(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		name := "without-late-diagnostic"
		if diagnostic {
			name = "with-late-diagnostic"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				f := newStoreHostFixture(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job", Steps: []nodewire.StepSpec{{ID: "body"}}}}}, teststore.WithClock(func() time.Time { return now }))
				input := storedExecutionInput(t, f, &ClaimedNodeHost{})
				ref := runspec.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: "job"}
				actor := executorActor(input)
				if granted, err := awaitStart(t.Context(), f.Tenant, runstate.CapacityBinding{RunID: string(ref.RunID), NodeID: "job", Generation: int64(actor.Generation)}, actor, nil); err != nil || !granted {
					t.Fatal(err)
				}
				started, err := f.Tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: &input.Attempt})
				if err != nil {
					t.Fatal(err)
				}
				principal, err := f.Store.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
				if err != nil {
					t.Fatal(err)
				}
				gate := make(chan struct{})
				fast := make(chan struct{})
				close(fast)
				uploadStarted := make(chan struct{})
				var first *fencedLogUploader
				opens := 0
				logs := &localLogs{run: input.Accepted.RunID, masker: secrets.NewMasker(), upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
					opens++
					up := &fencedLogUploader{tenant: f.Tenant, commit: store.SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30"}, gate: fast, wait: func() {}}
					if opens == 1 {
						first = up
						up.gate = gate
						close(uploadStarted)
					}
					return up, nil
				}}
				logs.bind(principal, int64(started.Attempt))
				if err := logs.write(t.Context(), input.Accepted.RunID, "job", nodewire.FrameLog{Message: "ordinary output must survive"}); err != nil {
					t.Fatal(err)
				}
				oldDone := logs.finish(t.Context(), "job")
				<-uploadStarted
				// safety: A queued execution_end record can arrive after node-specific detachment.
				if diagnostic {
					if err := logs.write(t.Context(), input.Accepted.RunID, "job", nodewire.FrameLog{Event: "lease_keeper", Attrs: []byte(`{"kind":"execution_end"}`)}); err != nil {
						t.Logf("late record refused safely: %v", err)
					}
				}
				<-logs.finish(t.Context(), "")
				close(gate)
				<-oldDone
				logs.flushes.Wait()
				if first.refused {
					t.Fatal("late diagnostic reopened the same attempt and committed segment 1 before the detached ordinary tail; ordinary segment was refused")
				}
			})
		})
	}
}
