package nodehost_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func TestTenAwaitsOnTenChildrenAllSucceed(t *testing.T) {
	s := start(t)
	service := newService(t)
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	done := make(chan int, 10)
	service.AwaitChild = func(ctx context.Context, _ capability.Authorized, run, _ string, _ time.Duration) (nodehost.ProcessChildResult, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nodehost.ProcessChildResult{}, ctx.Err()
		}
		return nodehost.ProcessChildResult{RunID: run, Status: "success"}, nil
	}
	bearer := admitService(t, s, service)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for i := range 10 {
		go openRequest(ctx, s, http.MethodGet, fmt.Sprintf("/node/v1/children/child-%d/await?wait_ms=60000", i), bearer, "", done)
	}
	for range 10 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("awaits did not enter")
		}
	}
	close(release)
	for range 10 {
		select {
		case status := <-done:
			if status != http.StatusOK {
				t.Fatalf("await=%d", status)
			}
		case <-ctx.Done():
			t.Fatal("await did not finish")
		}
	}
}

func TestAwaitEndsWhenTheGrantIsRevoked(t *testing.T) {
	for _, end := range []string{"revoke", "close"} {
		t.Run(end, func(t *testing.T) {
			s := start(t)
			service := newService(t)
			entered := make(chan struct{})
			stopped := make(chan error, 1)
			service.AwaitChild = func(ctx context.Context, _ capability.Authorized, _, _ string, _ time.Duration) (nodehost.ProcessChildResult, error) {
				close(entered)
				<-ctx.Done()
				stopped <- ctx.Err()
				return nodehost.ProcessChildResult{}, ctx.Err()
			}
			bearer, revoke, err := s.Admit(routeNode{Service: service})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan reply, 1)
			go func() { done <- ask(t, s, http.MethodGet, "/node/v1/children/child/await?wait_ms=60000", bearer, "") }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("await did not enter")
			}
			if end == "revoke" {
				revoke()
			} else if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-stopped:
				if err != context.Canceled {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("revocation did not stop await")
			}
			select {
			case got := <-done:
				wantRefusal(t, got, http.StatusConflict, "cancelled")
			case <-ctx.Done():
				t.Fatal("request did not join")
			}
		})
	}
}
