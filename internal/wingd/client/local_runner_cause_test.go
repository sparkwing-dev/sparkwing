package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
)

func TestLocalRunnerLifecycleKeepsCancellationCause(t *testing.T) {
	for _, phase := range []string{"watch", "drain"} {
		for _, retry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retry=%t", phase, retry), func(t *testing.T) {
				home := shortHome(t)
				cause := fmt.Errorf("owner stopped: %w", context.Canceled)
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				var returned atomic.Bool
				if retry {
					ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{PutIdleConn: func(err error) {
						if err != nil {
							t.Error(err)
						}
						returned.Store(true)
						cancel(cause)
					}})
				}
				serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if retry {
						http.Error(w, "retry", http.StatusServiceUnavailable)
						return
					}
					cancel(cause)
					<-r.Context().Done()
				}))
				client, err := NewLocalRunnerClient(home, "v1")
				if err != nil {
					t.Fatal(err)
				}
				if phase == "watch" {
					_, err = client.Watch(ctx, "instance", "active")
				} else {
					err = client.Drain(ctx, "instance")
				}
				if retry && !returned.Load() {
					t.Fatal("did not complete the refused response before cancellation")
				}
				if !errors.Is(err, cause) {
					t.Fatalf("lifecycle lost cancellation cause: %v", err)
				}
			})
		}
	}
}
