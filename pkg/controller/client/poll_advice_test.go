package client_test

import (
	"context"
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
