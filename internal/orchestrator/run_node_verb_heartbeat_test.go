package orchestrator

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type heartbeatTransport func(*http.Request) (*http.Response, error)

func (f heartbeatTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDispatchedClaimRenewsAsThePodStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		renewed := make(chan struct{}, 1)
		transport := heartbeatTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/run-1/nodes/build/heartbeat" {
				renewed <- struct{}{}
			}
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
		})
		start := time.Now()
		ctx, stop := nodeClaimKeeper(client.NewWithToken("http://controller.test", &http.Client{Transport: transport}, "tok"), "run-1", "build",
			store.NodeClaimFence{HolderID: "k8s-job:x", ClaimGeneration: 1}, time.Minute).Run(context.Background())
		defer stop()
		select {
		case <-renewed:
		case <-time.After(time.Second):
			t.Fatal("the first renewal never reached the controller")
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("the first renewal waited %s after the pod started", elapsed)
		}
		stop()
		if !errors.Is(context.Cause(ctx), context.Canceled) {
			t.Fatalf("stop cause = %v", context.Cause(ctx))
		}
	})
}

func TestDispatchedClaimStopsWhenHeartbeatsFailThroughLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := heartbeatTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody, Header: make(http.Header)}, nil
		})
		start := time.Now()
		ctx, stop := nodeClaimKeeper(client.NewWithToken("http://controller.test", &http.Client{Transport: transport}, "tok"), "run-1", "build",
			store.NodeClaimFence{HolderID: "k8s-job:x", ClaimGeneration: 1}, 10*time.Second).Run(context.Background())
		defer stop()
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Second):
			t.Fatal("claim survived its silent lease")
		}
		stop()
		if !errors.Is(context.Cause(ctx), lease.ErrSilent) || time.Since(start) != 10*time.Second {
			t.Fatalf("heartbeat failure left the node running: %v", context.Cause(ctx))
		}
	})
}

func TestDispatchedClaimStopsImmediatelyOnRefusal(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"conflict", http.StatusConflict, ""},
		{"revoked", http.StatusUnauthorized, `{"token_state":"revoked"}`},
		{"cancelled", http.StatusForbidden, `{"error":"claim_cancelled"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := heartbeatTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header)}, nil
				})
				start := time.Now()
				ctx, stop := nodeClaimKeeper(client.NewWithToken("http://controller.test", &http.Client{Transport: transport}, "tok"), "run-1", "build",
					store.NodeClaimFence{HolderID: "k8s-job:x", ClaimGeneration: 1}, time.Minute).Run(context.Background())
				defer stop()
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("refused claim kept running")
				}
				stop()
				cancelled := (*lease.CancelError)(nil)
				matched := errors.Is(context.Cause(ctx), lease.ErrLeaseLost)
				if test.name == "cancelled" {
					matched = errors.As(context.Cause(ctx), &cancelled)
				}
				if !matched || time.Since(start) != 0 {
					t.Fatalf("refused claim did not stop immediately: cause=%v delay=%s", context.Cause(ctx), time.Since(start))
				}
			})
		})
	}
}
