package acceptance_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type offlinePolicyTransport struct{ calls int }

func (t *offlinePolicyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, errors.New("offline")
}

func TestLocalVersionPolicyDoesNotNeedNetwork(t *testing.T) {
	transport := &offlinePolicyTransport{}
	config := engine.Config{CacheDirectory: t.TempDir(), BuildVersion: "v1.2.3", HTTPClient: &http.Client{Transport: transport}, LocalOnly: true}
	local, err := engine.NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	index, err := local.Index(t.Context(), now)
	if err != nil || !index.Index.Offline() || len(index.Notices) != 1 || index.Notices[0].Kind != engineid.NoticePolicyUnavailable || transport.calls != 0 {
		t.Fatalf("local policy dependency: %+v calls=%d: %v", index, transport.calls, err)
	}
	if _, err := engine.Evaluate(index.Index, "v1.2.3", now); err != nil {
		t.Fatal(err)
	}
	_, err = engine.EvaluateRunner(index.Index, "v1.2.3", now)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Unavailable {
		t.Fatalf("runner accepted missing policy: %v", err)
	}
	config.LocalOnly = false
	runner, err := engine.NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Index(t.Context(), now)
	if !errors.As(err, &failure) || failure.Code != apierr.Unavailable || transport.calls == 0 {
		t.Fatalf("controller runner failed open: %v calls=%d", err, transport.calls)
	}
}
