package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestChildAwaitPacesImmediatePendingResponses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		calls := 0
		process := &NativeProcessClient{run: "parent", transport: testEngine(t, func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.Query().Get("wait") != "1m0s" || request.Header.Get("Authorization") != "Bearer private-capability" {
				t.Fatalf("child observation lost its wait or credential: %s", request.URL)
			}
			return childPollResponse(t, "queued"), nil
		})}
		child, err := process.Child(t.Context(), "child", time.Minute)
		if err != nil || child.Status != "queued" {
			t.Fatalf("child await: %+v, %v", child, err)
		}
		if elapsed := time.Since(started); elapsed != time.Minute || calls < 2 || calls > 35 {
			t.Fatalf("pending await returned after %s with %d reads, want one minute with bounded polling", elapsed, calls)
		}
	})
}

func TestChildAwaitObservesCompletionAndCancellation(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "cancelled", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls := 0
				process := &NativeProcessClient{run: "parent", transport: testEngine(t, func(*http.Request) (*http.Response, error) {
					calls++
					if time.Since(started) >= 10*time.Second {
						if outcome == "cancel" {
							cancel()
						} else {
							return childPollResponse(t, outcome), nil
						}
					}
					return childPollResponse(t, "running"), nil
				})}
				child, err := process.Child(ctx, "child", time.Minute)
				if outcome == "cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancelled await: %v", err)
					}
				} else if err != nil || child.Status != outcome {
					t.Fatalf("child completion: %+v, %v", child, err)
				}
				if elapsed := time.Since(started); elapsed < 10*time.Second || elapsed > 15*time.Second || calls > 12 {
					t.Fatalf("completion after %s and %d polls", elapsed, calls)
				}
			})
		})
	}
}

func childPollResponse(t *testing.T, status string) *http.Response {
	t.Helper()
	body, err := json.Marshal(controllerwire.RunDetail{ID: "child", Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return engineResponse(http.StatusOK, string(body))
}
