package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/health"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type profileProbeResult struct {
	err       error
	Name      string `json:"name"`
	Status    string `json:"status"`
	Target    string `json:"target,omitempty"`
	Detail    string `json:"detail,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

func interpretHealthBody(r *profileProbeResult, resp *http.Response) (health.Response, bool) {
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		r.Status = "fail"
		r.err = &apierr.Error{Code: apierr.Unavailable, Message: "controller health check failed"}
		r.Detail = fmt.Sprintf("health returned %s: %s",
			resp.Status, strings.TrimSpace(string(raw)))
		return health.Response{}, true
	}
	body, err := health.Decode(resp.Body)
	if err != nil && !errors.Is(err, health.ErrNotContract) {
		r.Status = "fail"
		r.Detail = err.Error()
		r.err = &apierr.Error{Code: apierr.ProtocolError, Message: r.Detail}
		return health.Response{}, true
	}
	if body.Degraded() {
		r.Status = "warn"
		r.Detail = strings.Join(body.Problems, "; ")
		if r.Detail == "" {
			r.Detail = "service reports degraded"
		}
		return body, true
	}
	return body, false
}

func probeController(ctx context.Context, prof *namedProfile) profileProbeResult {
	r := profileProbeResult{Name: "controller", Target: prof.ControllerURL()}
	if prof.ControllerURL() == "" {
		r.Status = "fail"
		r.Detail = "no controller URL in profile"
		r.err = &apierr.Error{Code: apierr.BadRequest, Message: r.Detail}
		return r
	}
	start := time.Now()
	resp, err := httpGetNoAuth(ctx, strings.TrimRight(prof.ControllerURL(), "/")+"/api/v1/health")
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		r.err = &apierr.Error{Code: apierr.Unavailable, Message: r.Detail}
		return r
	}
	defer resp.Body.Close()
	body, stop := interpretHealthBody(&r, resp)
	if stop {
		return r
	}
	if body.Auth == "disabled" {
		r.Status = "warn"
		r.Detail = "serving unauthenticated: no tokens configured, every endpoint is open"
		return r
	}
	r.Status = "ok"
	return r
}

func probeAuth(ctx context.Context, prof *namedProfile) profileProbeResult {
	r := profileProbeResult{Name: "auth"}
	if prof.ControllerURL() == "" {
		r.Status = "skip"
		r.Detail = "controller URL not set"
		return r
	}
	if prof.ControllerToken() == "" {
		r.Status = "warn"
		r.Detail = "no token in profile (ok for unauthed local stacks)"
		return r
	}
	start := time.Now()
	err := client.CheckIdentity(ctx, client.EngineOptions{
		URL: prof.ControllerURL(), HTTP: &http.Client{Timeout: 10 * time.Second},
		Bearer: func(context.Context) (string, error) { return prof.ControllerToken(), nil },
	})
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		r.err = err
		return r
	}
	r.Status = "ok"
	r.Detail = "credential authenticated"
	return r
}

func httpGetNoAuth(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(req)
}
