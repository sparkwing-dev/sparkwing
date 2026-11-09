package nodehost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestAnAwaitHoldsTheNoProgressTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, activity, stop := executorProgressContext(t.Context(), time.Second)
		defer stop()
		routes, service, auth := serviceFixture(t)
		service.activity = &nativeProcessActivity{activity: activity}
		entered, finish := make(chan struct{}), make(chan struct{})
		service.AwaitChild = func(ctx context.Context, _ capability.Authorized, run, job string, wait time.Duration) (ProcessChildResult, error) {
			close(entered)
			select {
			case <-finish:
				return ProcessChildResult{RunID: run, Status: "success"}, nil
			case <-ctx.Done():
				return ProcessChildResult{}, context.Cause(ctx)
			}
		}
		done := make(chan error, 1)
		for _, endpoint := range routes.endpoints {
			if endpoint.Pattern != "/node/v1/children/{run}/await" {
				continue
			}
			go func() {
				request := httptest.NewRequest(http.MethodGet, "/node/v1/children/child/await?wait_ms=60000", nil).WithContext(ctx)
				request.SetPathValue("run", "child")
				done <- endpoint.Handle(httptest.NewRecorder(), request, auth)
			}()
		}
		<-entered
		silent, _, stopSilent := executorProgressContext(t.Context(), time.Second)
		defer stopSilent()
		<-silent.Done()
		if !errors.Is(context.Cause(silent), errExecutorNoProgress) || ctx.Err() != nil {
			t.Fatal("the authorized wait consumed the job's no-progress budget")
		}
		close(finish)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), errExecutorNoProgress) {
			t.Fatal("child completion did not resume the no-progress budget")
		}
	})
}

func TestAwaitSuspendsOnceAcrossNestedWaits(t *testing.T) {
	var got []bool
	parent := WithAwaitSlot(t.Context(), func() func(context.Context) {
		got = append(got, true)
		return func(context.Context) { got = append(got, false) }
	})
	_, activity, stop := executorProgressContext(parent, time.Minute)
	defer stop()
	first, second := activity.Await(), activity.Await()
	first()
	first()
	if len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("first returning wait did not reacquire capacity: %v", got)
	}
	second()
	if len(got) != 2 || got[1] {
		t.Fatalf("nested resumes changed the slot twice: %v", got)
	}
}

func TestOverlappingAwaitPollReleasesReacquiredSlot(t *testing.T) {
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	parent := WithAwaitSlot(t.Context(), func() func(context.Context) {
		<-slot
		return func(ctx context.Context) {
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
			}
		}
	})
	_, activity, stop := executorProgressContext(parent, 0)
	defer stop()
	first, second := activity.Await(), activity.Await()
	first()
	third := activity.Await()
	select {
	case slot <- struct{}{}:
		<-slot
	default:
		t.Fatal("repeated child poll held the only runner slot while another poll remained pending")
	}
	second()
	third()
}

func TestAwaitResumeKeepsIdleBudgetPausedUntilSlotReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slot := make(chan struct{})
		resuming := make(chan struct{})
		parent := WithAwaitSlot(t.Context(), func() func(context.Context) {
			return func(ctx context.Context) {
				close(resuming)
				select {
				case <-slot:
				case <-ctx.Done():
				}
			}
		})
		ctx, activity, stop := executorProgressContext(parent, time.Second)
		defer stop()
		resume := activity.Await()
		done := make(chan struct{})
		go func() { resume(); close(done) }()
		<-resuming
		timer := time.NewTimer(2 * time.Second)
		<-timer.C
		if ctx.Err() != nil {
			t.Fatal("capacity wait consumed the idle budget")
		}
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("await returned before acquiring a slot")
		default:
		}
		close(slot)
		<-done
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), errExecutorNoProgress) {
			t.Fatal(context.Cause(ctx))
		}
	})
}

func TestStoppingExecutorCancelsSlotReacquisition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resuming := make(chan struct{})
		parent := WithAwaitSlot(t.Context(), func() func(context.Context) {
			return func(ctx context.Context) { close(resuming); <-ctx.Done() }
		})
		_, activity, stop := executorProgressContext(parent, time.Second)
		resume := activity.Await()
		done := make(chan struct{})
		go func() { resume(); close(done) }()
		<-resuming
		stop()
		<-done
	})
}
