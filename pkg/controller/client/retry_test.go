package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
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

func TestExecutionStartRetriesTransientAnswers(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(unavailableOnce(&attempts, http.StatusNoContent))
	defer srv.Close()
	c := New(srv.URL, srv.Client())
	if err := c.AcknowledgeNodeExecutionStart(context.Background(), "r1", "n1", store.ExecutionStart{AttemptOrdinal: 1}); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("execution-start attempts = %d, want 2", got)
	}
}

func TestExecutionStartStopsAfterRetryBudget(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, srv.Client())
	if err := c.AcknowledgeNodeExecutionStart(context.Background(), "r1", "n1", store.ExecutionStart{AttemptOrdinal: 1}); err == nil {
		t.Fatal("execution-start succeeded against a controller that only answers 503")
	}
	if got := attempts.Load(); got != UnavailableRetries+1 {
		t.Fatalf("execution-start attempts = %d, want %d", got, UnavailableRetries+1)
	}
}

type transientRoundTripper func(*http.Request) (*http.Response, error)

func (f transientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestExecutionStartRetriesTransientTransportError(t *testing.T) {
	var attempts atomic.Int64
	transport := transientRoundTripper(func(req *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	})
	c := New("http://example.invalid", &http.Client{Transport: transport})
	if err := c.AcknowledgeNodeExecutionStart(context.Background(), "r1", "n1", store.ExecutionStart{AttemptOrdinal: 1}); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("execution-start attempts = %d, want 2", got)
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

func TestClaimShedByTheBudgetIsAnUnavailableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable","message":"authentication is busy, retry shortly"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	_, err := c.ClaimNodeWithCapacity(context.Background(), "holder", nil, time.Minute, nil, nil)
	var shed *UnavailableError
	if !errors.As(err, &shed) {
		t.Fatalf("ClaimNodeWithCapacity error = %v (%T), want an UnavailableError", err, err)
	}
	if shed.RetryAfter != time.Second {
		t.Fatalf("RetryAfter = %s, want 1s as the controller sent it", shed.RetryAfter)
	}
	if !strings.Contains(shed.Error(), "unavailable") {
		t.Fatalf("error text %q dropped the controller's reason", shed.Error())
	}
}

func TestUnavailableWithoutRetryAfterStaysAPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	_, err := c.ClaimNodeWithCapacity(context.Background(), "holder", nil, time.Minute, nil, nil)
	var shed *UnavailableError
	if errors.As(err, &shed) {
		t.Fatalf("a 503 that named no Retry-After became %v, want a plain error", err)
	}
	if err == nil {
		t.Fatal("a 503 claim returned no error")
	}
}

func TestListNodesHonorsPollAfterOn429(t *testing.T) {
	for _, header := range []string{"Retry-After", store.ClaimPollAfterHeader} {
		t.Run(header, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(header, "11")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer srv.Close()
			_, err := New(srv.URL, srv.Client()).ListNodes(context.Background(), "run-1")
			if wait, ok := LoadSignal(err); !ok || wait != 11*time.Second {
				t.Fatalf("429 %s = %s, %v; want 11s load signal", header, wait, ok)
			}
		})
	}
}
