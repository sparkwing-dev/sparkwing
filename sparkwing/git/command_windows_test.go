//go:build windows

package git

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitWindowsCancellationClosesHTTPDescendant(t *testing.T) {
	bound, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(bound)
	defer cancel()
	ready := make(chan struct{}, 1)
	disconnected := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ready <- struct{}{}
		select {
		case <-request.Context().Done():
			disconnected <- struct{}{}
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	done := make(chan error, 1)
	go func() {
		_, err := runGitEnv(ctx, "", promptlessEnv(), "ls-remote", server.URL+"/stalled.git")
		done <- err
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Git exited before HTTP readiness: %v", err)
	case <-bound.Done():
		t.Fatal("Git never reached HTTP server")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled Git succeeded")
		}
	case <-bound.Done():
		t.Fatal("Git cancellation retained output pipes")
	}
	select {
	case <-disconnected:
	case <-bound.Done():
		t.Fatal("HTTP Git descendant retained its connection")
	}
}
