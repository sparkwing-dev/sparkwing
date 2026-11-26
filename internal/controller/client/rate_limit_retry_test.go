package client

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func rateLimitedResponse(seconds string) *http.Response {
	response := engineResponse(http.StatusTooManyRequests, `{"error":{"code":"rate_limited","message":"team request budget exhausted","details":{"retry_after_ms":`+seconds+`000}}}`)
	response.Header.Set("Retry-After", seconds)
	return response
}

func TestRateLimitedNodeTransitionIsLeftToTheHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			attempts++
			return rateLimitedResponse("30"), nil
		})
		body := runstate.NodeCommandPayload{To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: new(runspec.AttemptOrdinal(1)), Extra: runstate.NodeCommandExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}}
		start := time.Now()
		var applied runstate.Applied
		err := c.request(t.Context(), "POST", "/api/v1/runs/{run}/local-host/nodes/{job}/transition", "/api/v1/runs/run/local-host/nodes/job/transition", "", body, &applied)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != hosterr.RateLimited || attempts != 1 || time.Since(start) != 0 {
			t.Fatalf("node transition = %v after %d attempts and %v, want the refusal at once", err, attempts, time.Since(start))
		}
	})
}

func unavailableResponse(seconds string) *http.Response {
	response := engineResponse(http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"request failed","details":{"retry_after_ms":`+seconds+`000}}}`)
	response.Header.Set("Retry-After", seconds)
	return response
}

func TestRefusedRetriesSpreadAboveRetryAfter(t *testing.T) {
	for name, refusal := range map[string]func(string) *http.Response{"rate limited": rateLimitedResponse, "unavailable": unavailableResponse} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var mu sync.Mutex
				arrivals := map[string][]time.Time{}
				c := testEngine(t, func(r *http.Request) (*http.Response, error) {
					mu.Lock()
					defer mu.Unlock()
					arrivals[r.URL.Path] = append(arrivals[r.URL.Path], time.Now())
					if len(arrivals[r.URL.Path]) == 1 {
						return refusal("1"), nil
					}
					return engineResponse(http.StatusNoContent, ""), nil
				})
				start := time.Now()
				var wg sync.WaitGroup
				for _, claim := range []string{"one", "two"} {
					wg.Go(func() {
						if err := c.request(t.Context(), "POST", "/api/v1/claims/{claim}/heartbeat", "/api/v1/claims/"+claim+"/heartbeat", "", struct{}{}, nil); err != nil {
							t.Errorf("%s: %v", claim, err)
						}
					})
				}
				wg.Wait()
				one, two := arrivals["/api/v1/claims/one/heartbeat"], arrivals["/api/v1/claims/two/heartbeat"]
				if len(one) != 2 || len(two) != 2 || one[1].Equal(two[1]) || one[1].Sub(start) < 1400*time.Millisecond || two[1].Sub(start) < 1400*time.Millisecond {
					t.Fatalf("retries %v and %v after a shared one-second Retry-After", one, two)
				}
			})
		})
	}
}
