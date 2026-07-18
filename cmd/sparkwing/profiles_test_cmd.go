// `sparkwing profiles test` -- one-shot health check for the selected
// profile. Probes controller reachability, auth, logs, and gitcache
// so operators can distinguish "my CLI is wrong" from "the controller
// is down" without running four curl commands by hand.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

// profileProbeResult is one row in the health report. Kept private to
// this file because the same struct feeds both the table rendering
// and the JSON output with identical field names.
type profileProbeResult struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // "ok" | "warn" | "fail" | "skip"
	Target    string `json:"target,omitempty"`
	Detail    string `json:"detail,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

type profileTestReport struct {
	Profile string               `json:"profile"`
	Probes  []profileProbeResult `json:"probes"`
	OK      bool                 `json:"ok"`
}

func runProfilesTest(args []string) error {
	fs := flag.NewFlagSet(cmdProfilesTest.Path, flag.ContinueOnError)
	on := fs.String("profile", "", "profile name (default: current default)")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdProfilesTest, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := profileTestReport{Profile: prof.Name, OK: true}

	report.Probes = append(report.Probes, probeController(ctx, prof))
	report.Probes = append(report.Probes, probeAuth(ctx, prof))
	report.Probes = append(report.Probes, probeLogs(ctx, prof))
	report.Probes = append(report.Probes, probeGitcache(ctx, prof))

	for _, p := range report.Probes {
		if p.Status == "fail" {
			report.OK = false
		}
	}

	if *outputFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if !report.OK {
			return errors.New("one or more probes failed")
		}
		return nil
	}

	fmt.Fprintf(os.Stdout, "profile: %s\n", prof.Name)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, p := range report.Probes {
		latency := ""
		if p.LatencyMS > 0 {
			latency = fmt.Sprintf("(%dms)", p.LatencyMS)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			p.Name, p.Status, orDash(p.Target), strings.TrimSpace(p.Detail), latency)
	}
	_ = tw.Flush()
	if !report.OK {
		return errors.New("one or more probes failed")
	}
	return nil
}

// healthResp is the uniform shape every sparkwing service returns on
// its /health endpoint. Services that don't self-diagnose issues
// leave problems empty; services that do (controller, logs-service,
// gitcache) surface "component: detail" strings so CLI tooling can
// bubble them up without a blanket outage banner.
type healthResp struct {
	Status   string   `json:"status"`
	Problems []string `json:"problems,omitempty"`
	// Auth is "enabled" or "disabled"; the controller sets it so
	// tooling can warn when a deployment is serving open. Empty from
	// services that don't report it.
	Auth string `json:"auth,omitempty"`
}

// interpretHealthBody reads a GET /health response and folds it into
// the probe result: non-200 → fail; 200 with "degraded" → warn +
// joined problems; 200 + ok → no-op. Returns the decoded body (so
// callers can inspect extra fields like auth) and true if the caller
// should stop (status was set to fail or warn).
func interpretHealthBody(r *profileProbeResult, resp *http.Response) (healthResp, bool) {
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		r.Status = "fail"
		r.Detail = fmt.Sprintf("health returned %s: %s",
			resp.Status, strings.TrimSpace(string(raw)))
		return healthResp{}, true
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		r.Status = "fail"
		r.Detail = fmt.Sprintf("read health body: %v", err)
		return healthResp{}, true
	}
	var body healthResp
	_ = json.Unmarshal(raw, &body)
	if body.Status == "degraded" || len(body.Problems) > 0 {
		r.Status = "warn"
		r.Detail = strings.Join(body.Problems, "; ")
		if r.Detail == "" {
			r.Detail = "service reports degraded"
		}
		return body, true
	}
	return body, false
}

// probeController GETs <controller>/api/v1/health. The route is
// explicitly unauthenticated (k8s livenessProbes can't carry bearer
// tokens), so a 200 here without a token is expected.
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

// probeAuth GETs /api/v1/runs?limit=1 with the profile's token. A 200
// means the token authenticates; 401/403 means a bad token; 5xx
// implies a controller issue already surfaced by probeController.
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
	r.Status = "ok"
	r.Detail = fmt.Sprintf("principal=%s, scopes=[%s]", orEmpty(who.Principal), strings.Join(sortedScopes(who.Scopes), ","))
	return r
}

// probeLogs reports the profile's logs backend. Logs route through the
// controller unless the profile declares an explicit logs: backend, so
// reachability is covered by the controller probe; this only describes
// where log bodies land.
func probeLogs(_ context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "logs", Target: profile.SpecString(prof.Logs)}
	if prof.Logs == nil {
		if prof.ControllerURL() != "" {
			r.Status = "ok"
			r.Detail = "logs route through the controller"
			return r
		}
		r.Status = "warn"
		r.Detail = "no logs: backend configured"
		return r
	}
	r.Status = "ok"
	r.Detail = "logs backend: " + profile.SpecString(prof.Logs)
	return r
}

// probeGitcache discovers the cache pod URL via the controller's
// /api/v1/services endpoint, then probes its /health. Missing
// controller or no announced cache pod => warn. interpretHealthBody
// surfaces background-fetch failures / cache-dir-unwritable problems
// to the operator as warnings.
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
		r.Status = "warn"
		r.Detail = "controller announced no cache pod URL"
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

// whoamiResp mirrors pkg/controller.whoamiResp. Duplicated (rather
// than imported) to keep the CLI binary from dragging the controller
// package and its storage deps.
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
