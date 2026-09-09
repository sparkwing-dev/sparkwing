//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type inspectionContextKey struct{}

func TestCancelledCallerStillTerminatesOwnedSession(t *testing.T) {
	command := exec.Command("/bin/sleep", "30")
	group, err := StartSession(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Second)
		defer cancel()
		if err := group.Terminate(ctx, time.Millisecond); errors.Is(err, ErrCleanup) {
			t.Errorf("cleanup session: %v", err)
		}
	})
	original := sessionProcessTable
	t.Cleanup(func() { sessionProcessTable = original })
	inspections := 0
	sessionProcessTable = func(ctx context.Context, sessions bool) ([]Info, error) {
		inspections++
		if ctx.Err() != nil || ctx.Value(inspectionContextKey{}) != "fixture" {
			return nil, errors.New("mandatory inspection lost its active caller context")
		}
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("mandatory inspection has no deadline")
		}
		return original(ctx, sessions)
	}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), inspectionContextKey{}, "fixture"))
	cancel()
	if err := group.Terminate(ctx, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("termination error = %v, want caller cancellation after signaling", err)
	}
	if inspections == 0 {
		t.Fatal("termination did not inspect the owned session")
	}
	select {
	case <-group.LeaderExited():
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled caller prevented leader termination")
	}
	sessionProcessTable = original
	finishCtx, finishCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer finishCancel()
	err = group.Finish(finishCtx, time.Millisecond)
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || !group.Reaped() || command.ProcessState == nil {
		t.Fatalf("terminated session result = %v, reaped=%v, state=%v", err, group.Reaped(), command.ProcessState)
	}
}

func TestMandatorySignalInspectionHasIndependentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		group := &Group{}
		err := group.signal(ctx, true, func(inspectionCtx context.Context, _ int, _, _ bool) error {
			if inspectionCtx.Err() != nil {
				return errors.New("caller cancellation reached mandatory signaling")
			}
			<-inspectionCtx.Done()
			return errors.Join(syscall.EIO, inspectionCtx.Err())
		})
		if !errors.Is(err, syscall.EIO) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("inspection error = %v, want native I/O and deadline causes", err)
		}
	})
}

func TestDescendantInspectionPreservesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), inspectionContextKey{}, "fixture"))
	cancel()
	group := &Group{}
	group.SetDescendantProbe(func(probeCtx context.Context, _ int, _, _ bool) (bool, error) {
		if probeCtx.Value(inspectionContextKey{}) != "fixture" || probeCtx.Err() == nil {
			return false, errors.New("descendant inspection lost caller context")
		}
		return false, probeCtx.Err()
	})
	if err := group.waitDescendantsEmpty(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("descendant inspection error = %v, want caller cancellation", err)
	}
}

func TestPSInspectionHonorsExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	processes, err := psProcessTable(ctx, false)
	if len(processes) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired process inspection = %+v, %v", processes, err)
	}
}
