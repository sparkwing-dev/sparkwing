package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/health"
)

// HealthService describes one component the dashboard can probe at
// /api/v1/health/services.
type HealthService struct {
	Name string
	URL  string
}

// serviceStatus mirrors web/src/lib/api.ts:ServiceStatus.
type serviceStatus struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Status    string   `json:"status"` // ok | degraded | down | unknown
	LatencyMs int64    `json:"latency_ms"`
	CheckedAt string   `json:"checked_at"`
	Error     string   `json:"error,omitempty"`
	Problems  []string `json:"problems,omitempty"`
}

// healthServicesHandler probes each configured service in parallel and
// returns the aggregated status. The 3s ctx timeout keeps slow services
// from stalling the whole response.
func healthServicesHandler(services []HealthService, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(services) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"services": []serviceStatus{}})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		out := make([]serviceStatus, len(services))
		var wg sync.WaitGroup
		for i, svc := range services {
			wg.Add(1)
			go func(i int, svc HealthService) {
				defer wg.Done()
				out[i] = probeService(ctx, svc, token)
			}(i, svc)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, map[string]any{"services": out})
	}
}

func probeService(ctx context.Context, svc HealthService, token string) serviceStatus {
	status := serviceStatus{
		Name:      svc.Name,
		URL:       svc.URL,
		Status:    "unknown",
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if svc.URL == "" {
		status.Status = "down"
		status.Error = "no URL configured"
		return status
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc.URL, nil)
	if err != nil {
		status.Status = "down"
		status.Error = err.Error()
		return status
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	status.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		status.Status = "down"
		status.Error = err.Error()
		return status
	}
	defer resp.Body.Close()
	// Drain whatever the branches below leave unread before the close:
	// a body abandoned mid-stream costs the pooled connection, and the
	// panel reprobes every service on a cycle. Deferred rather than
	// inline so the non-2xx branches, which read nothing, are covered
	// too; bounded because an endpoint answering with something huge is
	// not worth reading to the end to save one connection.
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, health.MaxBodyBytes)) }()
	switch {
	case resp.StatusCode == http.StatusOK:
		status.Status = "ok"
		applyHealthBody(&status, resp)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		status.Status = "degraded"
		status.Error = fmt.Sprintf("HTTP %d (auth wall)", resp.StatusCode)
	case resp.StatusCode >= 500:
		status.Status = "down"
		status.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	default:
		status.Status = "degraded"
		status.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	noteSlowResponse(&status)
	return status
}

// slowResponseMs is how long a health endpoint may take before the
// dashboard calls the service slow. Health handlers report bookkeeping
// a process already holds in memory; over a second and a half of it is
// a service under load, not a service doing work.
const slowResponseMs = 1500

// noteSlowResponse records the dashboard's own latency observation.
//
// It runs whatever the body said. Slowness is measured from here and
// nowhere else, so folding it into an already-degraded verdict would
// drop the one fault this vantage point can see behind the ones anybody
// reading /health can see for themselves -- and a service reporting
// problems is exactly the one likely to be slow as well.
//
// A service already down is exempt, whether it never answered or
// answered 5xx: the elapsed time is the timeout it hit or the cost of
// failing, and "slow" adds nothing to a lamp that is already red.
func noteSlowResponse(status *serviceStatus) {
	if status.Status == "down" || status.LatencyMs <= slowResponseMs {
		return
	}
	status.Status = "degraded"
	status.Problems = append(status.Problems,
		fmt.Sprintf("slow response: %dms", status.LatencyMs))
}

// applyHealthBody folds a 2xx health body into the probe result. Every
// sparkwing service reports partial failure in-body while answering
// 200 (see internal/health), so a probe that stops at the status code
// paints a green lamp over the very conditions those endpoints exist
// to report.
//
// A body that is not the JSON contract leaves the status alone: an
// endpoint outside the contract answering 200 has told us it is up and
// nothing more, and calling that degraded would redden every service
// that does not self-diagnose. A body that could not be read is left
// alone as well, unlike in `sparkwing configure profiles test`: that
// command answers an operator once and owes them a failure when it
// learned nothing, while the panel repaints from a fresh probe every
// cycle and would only flicker.
//
// The dashboard deliberately stops short of the CLI's extra rule that
// `auth: disabled` is a warning. That one is an operator-configuration
// judgement `sparkwing configure profiles test` makes about a profile
// it was pointed at, not a statement about the service's own health.
func applyHealthBody(status *serviceStatus, resp *http.Response) {
	body, err := health.Decode(resp.Body)
	if err != nil || !body.Degraded() {
		return
	}
	status.Status = "degraded"
	if len(body.Problems) == 0 {
		status.Problems = append(status.Problems, "service reports degraded")
		return
	}
	status.Problems = append(status.Problems, body.Problems...)
}

// defaultServices returns the baseline probe list from the dashboard's
// known service URLs. The cache is probed at /health rather than
// /api/v1/health because that is the route it serves.
func defaultServices(opts HandlerOptions, logsURL string) []HealthService {
	var out []HealthService
	if opts.ControllerURL != "" {
		out = append(out, HealthService{
			Name: "controller",
			URL:  healthURL(opts.ControllerURL, "/api/v1/health"),
		})
	}
	if logsURL != "" {
		out = append(out, HealthService{
			Name: "logs",
			URL:  healthURL(logsURL, "/api/v1/health"),
		})
	}
	if opts.CacheURL != "" {
		out = append(out, HealthService{
			Name: "cache",
			URL:  healthURL(opts.CacheURL, "/health"),
		})
	}
	return out
}

// healthURL joins a configured base URL to a service's health route
// without doubling the separator when the operator wrote a trailing
// slash.
func healthURL(base, route string) string {
	return strings.TrimRight(base, "/") + route
}
