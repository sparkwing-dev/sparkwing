package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPollAdvice_ReadsTheControllerSuggestionOffAClaim(t *testing.T) {
	advice := "12"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if advice != "" {
			w.Header().Set(store.ClaimPollAfterHeader, advice)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := client.New(ts.URL, nil)
	if _, err := c.ClaimNode(context.Background(), "runner-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if got := c.PollAdvice(); got != 12*time.Second {
		t.Errorf("PollAdvice=%s want 12s", got)
	}

	advice = ""
	if _, err := c.ClaimNode(context.Background(), "runner-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if got := c.PollAdvice(); got != 0 {
		t.Errorf("PollAdvice=%s want 0 once the controller stops suggesting one", got)
	}
}

func TestPollAdvice_CapsAnImplausibleSuggestion(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(store.ClaimPollAfterHeader, "3600")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := client.New(ts.URL, nil)
	if _, err := c.ClaimTriggerFor(context.Background(), nil, nil); err != nil {
		t.Fatalf("ClaimTriggerFor: %v", err)
	}
	if got := c.PollAdvice(); got != client.MaxPollAdvice {
		t.Errorf("PollAdvice=%s want %s", got, client.MaxPollAdvice)
	}
}

func TestLoadSignal_ReadsA429AsBackpressure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"too many claim requests from this runner"}`))
	}))
	defer ts.Close()

	c := client.New(ts.URL, nil)
	_, err := c.ClaimNode(context.Background(), "runner-1", nil, time.Minute, nil)
	if err == nil {
		t.Fatal("a shed claim reported no error")
	}
	var limited *client.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("err = %T (%v); want a RateLimitedError a claim loop can back off on", err, err)
	}
	wait, ok := client.LoadSignal(err)
	if !ok || wait != 7*time.Second {
		t.Errorf("LoadSignal = %s, %v; want 7s, true", wait, ok)
	}
}
