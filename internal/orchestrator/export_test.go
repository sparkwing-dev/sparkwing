package orchestrator

import (
	"log/slog"
	"net/http"
)

func NewHTTPLogs(baseURL string, httpClient *http.Client, logger *slog.Logger) *HTTPLogs {
	return NewHTTPLogsWithToken(baseURL, httpClient, "", logger)
}
