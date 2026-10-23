package client

import (
	"context"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (c *engineClient) RecordStep(ctx context.Context, run, job string, input controllerwire.StepReportRequest) error {
	if !validSelector(run) || !validSelector(job) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid run or job selector"}
	}
	action, err := input.Action()
	if err != nil {
		return err
	}
	var result controllerwire.StepRecorded
	err = c.request(ctx, "POST", "/api/v1/runs/{run}/jobs/{job}/steps/"+action,
		"/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/steps/"+action, "", input, &result)
	if err != nil {
		return err
	}
	if !result.Recorded {
		return &apierr.Error{Code: apierr.ProtocolError, Message: "controller did not acknowledge the step report"}
	}
	return nil
}
