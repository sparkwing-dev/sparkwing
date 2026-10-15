package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/health"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

type profileProbeResult struct {
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
		r.Detail = fmt.Sprintf("health returned %s: %s",
			resp.Status, strings.TrimSpace(string(raw)))
		return health.Response{}, true
	}
	body, err := health.Decode(resp.Body)
	if err != nil && !errors.Is(err, health.ErrNotContract) {
		r.Status = "fail"
		r.Detail = err.Error()
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

func probeController(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "controller", Target: prof.ControllerURL()}
	if prof.ControllerURL() == "" {
		r.Status = "fail"
		r.Detail = "no controller URL in profile"
		return r
	}
	start := time.Now()
	resp, err := httpGetNoAuth(ctx, strings.TrimRight(prof.ControllerURL(), "/")+"/api/v1/health")
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
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

func probeAuth(ctx context.Context, prof *profile.Profile) profileProbeResult {
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
	resp, err := httpGetWithToken(ctx, strings.TrimRight(prof.ControllerURL(), "/")+"/api/v1/runs?limit=1", prof.ControllerToken())
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		r.Status = "fail"
		r.Detail = fmt.Sprintf("token rejected (%s)", resp.Status)
		return r
	default:
		r.Status = "fail"
		r.Detail = fmt.Sprintf("runs endpoint returned %s", resp.Status)
		return r
	}

	r.Status = "ok"
	r.Detail = "credential can read runs"
	return r
}

func httpGetNoAuth(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}

func httpGetWithToken(ctx context.Context, url, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}
