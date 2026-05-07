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

	"github.com/sparkwing-dev/sparkwing/v2/profile"
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
	on := fs.String("on", "", "profile name (default: current default)")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
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

	// Probe order is fixed so operators reading the table top-to-
	// bottom see reachability -> auth -> aux services. Each probe is
	// independent; downstream probes still run even when an earlier
	// one fails, because a 401 on /runs still tells us the controller
	// is reachable even though /health failed.
	report.Probes = append(report.Probes, probeController(ctx, prof))
	report.Probes = append(report.Probes, probeAuth(ctx, prof))
	report.Probes = append(report.Probes, probeLogs(ctx, prof))
	report.Probes = append(report.Probes, probeGitcache(ctx, prof))

	for _, p := range report.Probes {
		if p.Status == "fail" {
			report.OK = false
		}
	}

	if *asJSON || *outputFormat == "json" {
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
}

// interpretHealthBody reads a GET /health response and folds it into
// the probe result: non-200 → fail; 200 with "degraded" → warn +
// joined problems; 200 + ok → no-op. Returns true if the caller
// should stop (status was set to fail or warn).
func interpretHealthBody(r *profileProbeResult, resp *http.Response) bool {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		r.Status = "fail"
		r.Detail = fmt.Sprintf("health returned %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		r.Status = "fail"
		r.Detail = fmt.Sprintf("read health body: %v", err)
		return true
	}
	var body healthResp
	// Best-effort decode: services that predate the shape return an
	// empty body or a plain `{"status":"ok"}`; treat unparseable as
	// "ok, no self-report available" rather than flagging operators
	// about a server that isn't telling them anything actionable.
	_ = json.Unmarshal(raw, &body)
	if body.Status == "degraded" || len(body.Problems) > 0 {
		r.Status = "warn"
		r.Detail = strings.Join(body.Problems, "; ")
		if r.Detail == "" {
			r.Detail = "service reports degraded"
		}
		return true
	}
	return false
}

// probeController GETs <controller>/api/v1/health. The route is
// explicitly unauthenticated (k8s livenessProbes can't carry bearer
// tokens), so a 200 here without a token is expected.
func probeController(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "controller", Target: prof.Controller}
	if prof.Controller == "" {
		r.Status = "fail"
		r.Detail = "no controller URL in profile"
		return r
	}
	start := time.Now()
	resp, err := httpGetNoAuth(ctx, strings.TrimRight(prof.Controller, "/")+"/api/v1/health")
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	defer resp.Body.Close()
	if interpretHealthBody(&r, resp) {
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
	if prof.Controller == "" {
		r.Status = "skip"
		r.Detail = "controller URL not set"
		return r
	}
	if prof.Token == "" {
		// Missing token is a warn, not a fail: laptop dev mode runs
		// with auth disabled, and that's a legitimate configuration.
		r.Status = "warn"
		r.Detail = "no token in profile (ok for unauthed local stacks)"
		return r
	}
	start := time.Now()
	resp, err := httpGetWithToken(ctx, strings.TrimRight(prof.Controller, "/")+"/api/v1/runs?limit=1", prof.Token)
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// Fall through -- we'll enrich with whoami below.
	case http.StatusUnauthorized, http.StatusForbidden:
		r.Status = "fail"
		r.Detail = fmt.Sprintf("token rejected (%s)", resp.Status)
		return r
	default:
		r.Status = "fail"
		r.Detail = fmt.Sprintf("runs endpoint returned %s", resp.Status)
		return r
	}

	// Enrich with principal + scopes via /auth/whoami. If whoami is
	// down but /runs succeeded, keep status=ok and note the degraded
	// introspection -- the important signal (token works) is green.
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

// probeLogs GETs <logs>/health when the profile carries a logs URL.
// Missing URL => warn (logs are optional; some operators only use
// the controller's built-in log tail).
func probeLogs(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "logs", Target: prof.Logs}
	if prof.Logs == "" {
		r.Status = "warn"
		r.Detail = "logs URL not set in profile"
		return r
	}
	start := time.Now()
	resp, err := httpGetNoAuth(ctx, strings.TrimRight(prof.Logs, "/")+"/api/v1/health")
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	defer resp.Body.Close()
	if interpretHealthBody(&r, resp) {
		return r
	}
	r.Status = "ok"
	return r
}

// probeGitcache probes <gitcache>/health when set. Gitcache is
// optional per-profile (only cluster-mode laptop dispatch needs it),
// so missing => warn. Gitcache was the first service to adopt the
// problems[] shape (background-fetch failures, cache dir unwritable);
// interpretHealthBody surfaces those to the operator as warnings.
func probeGitcache(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "gitcache", Target: prof.Gitcache}
	if prof.Gitcache == "" {
		r.Status = "warn"
		r.Detail = "gitcache URL not set in profile"
		return r
	}
	start := time.Now()
	resp, err := httpGetNoAuth(ctx, strings.TrimRight(prof.Gitcache, "/")+"/health")
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	defer resp.Body.Close()
	if interpretHealthBody(&r, resp) {
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
	resp, err := httpGetWithToken(ctx, strings.TrimRight(prof.Controller, "/")+"/api/v1/auth/whoami", prof.Token)
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
