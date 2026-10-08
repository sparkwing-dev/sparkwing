package orchestrator

import (
	"context"
	"log/slog"
	"net/http"
)

func NewHTTPLogs(baseURL string, httpClient *http.Client, logger *slog.Logger) *HTTPLogs {
	return NewHTTPLogsWithToken(baseURL, httpClient, "", logger)
}

type HostedRun = hostedRun

type HostedRunResult = hostedRunResult

func RunHosted(ctx context.Context, cfg HostedRun) (HostedRunResult, error) {
	return runHosted(ctx, cfg)
}
