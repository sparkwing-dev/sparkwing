package orchestrator

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func DumpRunState(ctx context.Context, st *store.Store, runID string, art storage.ArtifactStore) error {
	return dumpRunState(ctx, localState{st: st}, runID, art)
}

func NewHTTPLogs(baseURL string, httpClient *http.Client, logger *slog.Logger) *HTTPLogs {
	return NewHTTPLogsWithToken(baseURL, httpClient, "", logger)
}

type HostedRun = hostedRun

type HostedRunResult = hostedRunResult

func RunHosted(ctx context.Context, cfg HostedRun) (HostedRunResult, error) {
	return runHosted(ctx, cfg)
}
