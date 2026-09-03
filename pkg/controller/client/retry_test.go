package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestClientRetriesAnUnavailableAnswerThatNamesRetryAfter(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	if err := c.CreateRun(context.Background(), store.Run{ID: "r1", Pipeline: "p"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("server saw %d attempt(s), want the first refused and the second served", got)
	}
}

func TestClientDoesNotRetryAnUnavailableAnswerWithNoRetryAfter(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	if err := c.CreateRun(context.Background(), store.Run{ID: "r1", Pipeline: "p"}); err == nil {
		t.Fatal("CreateRun succeeded against a server that only answers 503")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("server saw %d attempt(s), want one; a 503 with no Retry-After is terminal", got)
	}
}

func TestClientStopsRetryingAtTheBound(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p"}); err == nil {
		t.Fatal("CreateRun succeeded against a server that always answers 503")
	}
	if got := attempts.Load(); got != UnavailableRetries+1 {
		t.Fatalf("server saw %d attempt(s), want %d", got, UnavailableRetries+1)
	}
}
