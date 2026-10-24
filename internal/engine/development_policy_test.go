package engine

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func TestDevelopmentPolicySkipsIndexAndRunnerUpdates(t *testing.T) {
	transport := &fixtureTransport{failure: true}
	r, err := NewResolver(Config{BuildVersion: "(devel)", CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	index, err := r.Index(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, evaluate := range []func(VerifiedIndex, string, time.Time) ([]engineid.Notice, error){Evaluate, EvaluateRunner} {
		if notices, err := evaluate(index.Index, "(devel)", time.Now()); err != nil || len(notices) != 0 {
			t.Fatalf("development evaluation: %+v: %v", notices, err)
		}
	}
	update, err := r.PrepareRunnerUpdate(t.Context(), "(devel)", releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: "linux", GOARCH: "amd64"})
	if err != nil || update.Version != "(devel)" || update.Required || len(transport.requests) != 0 {
		t.Fatalf("development update fetched policy or replaced the runner: %+v: %v", update, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Index(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
