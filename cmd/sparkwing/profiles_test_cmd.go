package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/health"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type profileProbeResult struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Target    string `json:"target,omitempty"`
	Detail    string `json:"detail,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

type profileTestReport struct {
	Profile string               `json:"profile"`
	Probes  []profileProbeResult `json:"probes"`
	OK      bool                 `json:"ok"`
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

	who, err := fetchWhoami(ctx, prof)
	if err != nil {
		r.Status = "ok"
		r.Detail = "token authenticates (whoami unavailable)"
		return r
	}
	if who.Principal == authwire.AnonymousPrincipal || who.Kind == authwire.AnonymousKind {
		r.Status = "warn"
		r.Detail = "serving unauthenticated: no tokens configured, the controller accepts any bearer"
		return r
	}
	r.Status = "ok"
	r.Detail = fmt.Sprintf("principal=%s, scopes=[%s]", orEmpty(who.Principal), strings.Join(sortedScopes(who.Scopes), ","))
	return r
}

func probeLogs(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "logs", Target: profile.SpecString(prof.Logs)}
	if prof.Logs != nil && prof.Logs.Type != backends.TypeController {
		r.Status = "ok"
		r.Detail = "logs backend: " + profile.SpecString(prof.Logs)
		return r
	}
	if !prof.HasController() {
		r.Status = "warn"
		r.Detail = "no logs: backend configured"
		return r
	}
	keeps, err := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()).KeepsLogSegments(ctx)
	switch {
	case err != nil:
		r.Status = "warn"
		r.Detail = "could not ask the controller whether it keeps node logs: " + err.Error()
	case !keeps:
		r.Status = "warn"
		r.Detail = "the controller keeps no node logs: it needs a cache bucket or an output directory"
	default:
		r.Status = "ok"
		r.Detail = "node logs reach the controller as segments"
	}
	return r
}

func probeGitcache(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "gitcache"}
	if !prof.HasController() {
		r.Status = "warn"
		r.Detail = "no controller on profile (gitcache URL is discovered via controller)"
		return r
	}
	services, err := discovery.ServicesFor(ctx, prof.ControllerURL(), prof.ControllerToken())
	if err != nil {
		r.Status = "fail"
		r.Detail = "discover services: " + err.Error()
		return r
	}
	r.Target = services.CachePod
	if services.CachePod == "" {
		r.Status = "skip"
		return r
	}
	start := time.Now()
	resp, err := httpGetNoAuth(ctx, strings.TrimRight(services.CachePod, "/")+"/health")
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	defer resp.Body.Close()
	if _, stop := interpretHealthBody(&r, resp); stop {
		return r
	}
	r.Status = "ok"
	return r
}

type whoamiResp struct {
	Principal   string   `json:"principal"`
	Kind        string   `json:"kind"`
	Scopes      []string `json:"scopes"`
	TokenPrefix string   `json:"token_prefix,omitempty"`
}

func fetchWhoami(ctx context.Context, prof *profile.Profile) (*whoamiResp, error) {
	resp, err := httpGetWithToken(ctx, strings.TrimRight(prof.ControllerURL(), "/")+"/api/v1/auth/whoami", prof.ControllerToken())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out whoamiResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
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

func orEmpty(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func sortedScopes(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
