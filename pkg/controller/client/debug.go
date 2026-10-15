package client

import (
	"context"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.DebugReader = (*engineClient)(nil)

// NewDebugReader uses the shared authenticated transport for public debug reads.
func NewDebugReader(options EngineOptions) (controllerwire.DebugReader, error) {
	return newEngineClient(options)
}

func (c *engineClient) ReadDebugJob(ctx context.Context, run, job string) (controllerwire.DebugJob, error) {
	var result controllerwire.DebugJob
	path, err := debugJobPath(run, job)
	if err != nil {
		return result, err
	}
	err = c.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}", path, "", nil, &result)
	if err == nil {
		err = result.Validate(run, job)
	}
	return result, err
}

func (c *engineClient) ReadDebugPause(ctx context.Context, run, job string) (controllerwire.DebugPauseResult, error) {
	var result controllerwire.DebugPauseResult
	path, err := debugJobPath(run, job)
	if err != nil {
		return result, err
	}
	err = c.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}/debug-pause", path+"/debug-pause", "", nil, &result)
	if err == nil {
		err = result.Validate(run, job)
	}
	return result, err
}

func debugJobPath(run, job string) (string, error) {
	if !validSelector(run) || !validSelector(job) {
		return "", &apierr.Error{Code: apierr.BadRequest, Message: "debug read requires run and job selectors"}
	}
	return "/api/v1/runs/" + url.PathEscape(run) + "/jobs/" + url.PathEscape(job), nil
}
