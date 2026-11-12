package nodehost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type preparationFinishStore struct {
	OwnerStore
	finish func(context.Context, runstate.FinishRunRequest) (runstate.Applied, error)
}

func (s preparationFinishStore) FinishRunAndUnit(ctx context.Context, r runstate.FinishRunRequest) (runstate.Applied, error) {
	return s.finish(ctx, r)
}

func TestPreparationFinishStopsAtAuthorityExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		keeper := lease.Keeper{Interval: time.Second, Lease: time.Minute, Renew: func(context.Context) (lease.Beat, error) {
			return lease.Beat{ExpiresAt: start.Add(250 * time.Millisecond), CancelRequested: true}, nil
		}}
		ctx, stop := keeper.Run(t.Context())
		defer stop()
		synctest.Wait()
		calls, late := 0, 0
		engine, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: stateRestartTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if time.Since(start) >= 250*time.Millisecond {
				late++
			}
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no backend"))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		host := OwnerHost{Masker: testMasker(), Store: preparationFinishStore{finish: func(ctx context.Context, _ runstate.FinishRunRequest) (runstate.Applied, error) {
			_, err := engine.Finish(ctx, "run", controllerwire.FinishRequest{Reason: "cancelled preparation"})
			return runstate.Applied{}, err
		}}}
		err = host.preparationFailure(ctx, RunInput{RunID: "run"}, store.RunRow{}, &lease.CancelError{Reason: "cancel_requested"})
		t.Logf("calls=%d late=%d elapsed=%s authority=%v error=%v", calls, late, time.Since(start), context.Cause(lease.ExecutionContext(ctx)), err)
		if calls != 1 || late != 0 || time.Since(start) != 250*time.Millisecond || !errors.Is(err, lease.ErrSilent) {
			t.Fatal("preparation finish retried after authority expired")
		}
	})
}
