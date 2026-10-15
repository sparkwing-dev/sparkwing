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
