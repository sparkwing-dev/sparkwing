package client

import (
	"context"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.RunnerReader = (*engineClient)(nil)

// NewRunnerReader reads team-scoped public runner details using the same
// credentials and retry policy as other controller observations.
func NewRunnerReader(options EngineOptions) (controllerwire.RunnerReader, error) {
	return newEngineClient(options)
}

func (c *engineClient) ListRunners(ctx context.Context) (controllerwire.Runners, error) {
	var result controllerwire.Runners
	if err := c.requestList(ctx, "/api/v1/runners", "/api/v1/runners", &result, "runners"); err != nil {
		return result, err
	}
	for _, runner := range result.Runners {
		if !validRunnerDetail(runner) {
			return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned invalid runner details"}
		}
	}
	return result, nil
}

func validRunnerDetail(runner controllerwire.RunnerDetail) bool {
	if !validSelector(runner.Name) || !validSelector(runner.Class) || runner.Location != "" && !validSelector(runner.Location) || !validSelector(runner.Status) || runner.MaxConcurrent < 0 || !validRunnerResources(runner.Capacity) || !validRunnerTimestamp(runner.LastSeen, true) {
		return false
	}
	for _, values := range [][]string{runner.Labels, runner.ReservedPools, runner.Repos} {
		for _, value := range values {
			if !validSelector(value) {
				return false
			}
		}
	}
	for _, job := range runner.ActiveJobs {
		if !validSelector(job.RunID) || job.JobID != "" && !validSelector(job.JobID) {
			return false
		}
	}
	h := runner.Headroom
	return h == nil || validRunnerResources(h.RunnerResources) && h.QueueDepth >= 0 && validRunnerTimestamp(h.ObservedAt, false)
}

func validRunnerResources(resources controllerwire.RunnerResources) bool {
	return resources.Cores >= 0 && !math.IsInf(resources.Cores, 0) && !math.IsNaN(resources.Cores) && resources.MemoryBytes >= 0
}

func validRunnerTimestamp(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
