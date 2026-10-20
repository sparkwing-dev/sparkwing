package cluster

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type dockerTransport func(*http.Request) (*http.Response, error)

func (f dockerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStopDockerKillsEveryContainer(t *testing.T) {
	var calls []string
	client := &http.Client{Transport: dockerTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		status, body := http.StatusNoContent, ""
		if r.Method == http.MethodGet {
			status, body = http.StatusOK, `[{"Id":"first"},{"Id":"second"}]`
		}
		if r.URL.Path == "/containers/first/kill" {
			status = http.StatusInternalServerError
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	err := stopDocker(t.Context(), client)
	want := []string{"GET /containers/json", "POST /containers/first/kill?signal=KILL", "POST /containers/second/kill?signal=KILL"}
	if err == nil || !slices.Equal(calls, want) {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
}

func TestStopDockerBoundsBlockedAPI(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		client := &http.Client{Transport: dockerTransport(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, context.Cause(r.Context())
		})}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		err := stopDocker(ctx, client)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 2*time.Second {
			t.Fatalf("elapsed=%v err=%v", time.Since(start), err)
		}
	})
}
