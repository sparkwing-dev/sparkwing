package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
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
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusOK:
		status.Status = "ok"
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
	// Slow-but-ok responses degrade so latency issues surface before
	// they become failures.
	if status.Status == "ok" && status.LatencyMs > 1500 {
		status.Status = "degraded"
		status.Problems = append(status.Problems,
			fmt.Sprintf("slow response: %dms", status.LatencyMs))
	}
	return status
}

// defaultServices returns the baseline probe list from the dashboard's
// known service URLs.
func defaultServices(opts HandlerOptions, logsURL string) []HealthService {
	var out []HealthService
	if opts.ControllerURL != "" {
		out = append(out, HealthService{
			Name: "controller",
			URL:  opts.ControllerURL + "/api/v1/health",
		})
	}
	if logsURL != "" {
		out = append(out, HealthService{
			Name: "logs",
			URL:  logsURL + "/api/v1/health",
		})
	}
	return out
}
