package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func unavailableOnce(attempts *atomic.Int64, served int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(served)
		_, _ = w.Write([]byte("{}"))
	}
}

func TestClientRetriesAnUnavailableReadThatNamesRetryAfter(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(unavailableOnce(&attempts, http.StatusOK))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	if _, err := c.GetRun(context.Background(), "r1"); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("server saw %d attempt(s), want the first refused and the second served", got)
	}
}

func TestClientDoesNotRetryAnUnavailablePost(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(unavailableOnce(&attempts, http.StatusCreated))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	if err := c.CreateRun(context.Background(), store.Run{ID: "r1", Pipeline: "p"}); err == nil {
		t.Fatal("a POST answered 503 was replayed; a repeated submission is a second run")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("server saw %d attempt(s) of a POST, want one", got)
	}
}

func TestClientRetriesOnlyRepeatableMethods(t *testing.T) {
	cases := []struct {
		method string
		want   bool
	}{
		{http.MethodGet, true},
		{http.MethodHead, true},
		{http.MethodPut, true},
		{http.MethodDelete, true},
		{http.MethodPost, false},
		{http.MethodPatch, false},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, "http://example/x", strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("build the request: %v", err)
			}
			if got := repeatable(req); got != tc.want {
				t.Fatalf("repeatable(%s) = %v, want %v", tc.method, got, tc.want)
			}
		})
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
	if _, err := c.GetRun(context.Background(), "r1"); err == nil {
		t.Fatal("GetRun succeeded against a server that only answers 503")
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
	if _, err := c.GetRun(ctx, "r1"); err == nil {
		t.Fatal("GetRun succeeded against a server that always answers 503")
	}
	if got := attempts.Load(); got != UnavailableRetries+1 {
		t.Fatalf("server saw %d attempt(s), want %d", got, UnavailableRetries+1)
	}
}
