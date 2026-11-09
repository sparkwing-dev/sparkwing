package nodehost

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestStartupRefusalAfterCancellationReportsCancelledBeforeHeartbeat(t *testing.T) {
	for _, stage := range []string{"storage", "process"} {
		t.Run(stage, func(t *testing.T) {
			f := newStoreHostFixture(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job", Steps: []nodewire.StepSpec{{ID: "body"}}}}})
			input := storedExecutionInput(t, f, &ClaimedNodeHost{})
			input.Entry = &source.Entry{Engine: input.Engine}
			actor := executorActor(input)
			if err := awaitStart(t.Context(), f.Tenant, runstate.CapacityBinding{RunID: input.Accepted.RunID, NodeID: input.NodeID, Generation: int64(input.Generation)}, actor, nil); err != nil {
				t.Fatal(err)
			}
			_, _, deps, processes := executorFixture(t)
			deps.Store = f.Tenant
			refuse := func(ctx context.Context) error {
				if _, err := f.Tenant.ResolveBlobAuthority(ctx, blob.Authority{CapabilityRef: input.Authority.CapabilityPrefix}, capability.DataRead); err != nil {
					t.Fatalf("startup authority was already refused: %v", err)
				}
				run, err := f.Tenant.ReadRun(ctx, input.Accepted.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.Tenant.TransitionRun(ctx, runstate.RunTransition{RunID: runspec.RunID(input.Accepted.RunID), Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: run.Request.AuthorityRef}, To: runstate.RunCancelled, Cause: runstate.RunCancel}); err != nil {
					t.Fatal(err)
				}
				_, err = f.Tenant.ResolveBlobAuthority(ctx, blob.Authority{CapabilityRef: input.Authority.CapabilityPrefix}, capability.DataRead)
				var refused *store.ErrIllegalTransition
				if !errors.As(err, &refused) || refused.Reason != store.ReasonHolderRights {
					t.Fatalf("cancellation did not refuse startup authority: %v", err)
				}
				return &apierr.Error{Code: apierr.LockHeld, Message: "state transition refused"}
			}
			if stage == "storage" {
				deps.BindStorage = func(ctx context.Context, _ NodeInput) (StorageBinding, error) { return StorageBinding{}, refuse(ctx) }
			} else {
				deps.Bind = func(ctx context.Context, _ NodeInput) (ProcessGrant, error) { return ProcessGrant{}, refuse(ctx) }
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(t.Context(), input)
			row, readErr := f.Tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)})
			var cancelled *lease.CancelError
			if readErr != nil || row.Outcome != runstate.NodeCancelled || row.FailureReason != string(apierr.ReasonCancelled) || !errors.As(err, &cancelled) || len(*processes) != 0 {
				t.Fatalf("startup cancellation became a failure: outcome=%s reason=%s processes=%d error=%v read=%v", row.Outcome, row.FailureReason, len(*processes), err, readErr)
			}
		})
	}
}

func TestStartupStorageRefusalSealsEmptyAttemptLogs(t *testing.T) {
	cfg, _ := nativeConfigFixture(t)
	cfg.Storage = func(context.Context, capability.Principal) (StorageBinding, error) {
		return StorageBinding{}, &apierr.Error{Code: apierr.LockHeld, Message: "state transition refused"}
	}
	records := &nativeLogRecords{}
	cfg.Logs = func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		records.view.Subject = subject
		return records, nil
	}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return &processListener{closed: make(chan struct{})}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.Config.Executor.BindStorage(t.Context(), NodeInput{NodeID: "job", Generation: 2, Attempt: 3}); err == nil {
		t.Fatal("storage refusal was ignored")
	}
	if err := services.Close(); err != nil {
		t.Fatal(err)
	}
	if !records.view.Complete || records.view.Subject.Attempt == nil || records.view.Subject.Attempt.Attempt != 3 {
		t.Fatalf("startup left an unsealed attempt log: %+v", records.view)
	}
}
