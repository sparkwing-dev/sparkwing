package client

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// NewLocalScheduleManager uses the shared authenticated local controller transport.
func NewLocalScheduleManager(options EngineOptions) (controllerwire.LocalScheduleManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) RegisterLocalSchedules(ctx context.Context, input controllerwire.LocalScheduleRegistration) (controllerwire.ScheduleRepoWritten, error) {
	var result controllerwire.ScheduleRepoWritten
	err := c.request(ctx, "PUT", "/api/v1/schedules/local/repos", "/api/v1/schedules/local/repos", "", input, &result)
	return result, err
}

func (c *engineClient) SetLocalSchedulePinned(ctx context.Context, name string, pin bool) (controllerwire.ScheduleView, error) {
	action := "/unpin"
	if pin {
		action = "/pin"
	}
	return c.scheduleEdit(ctx, "POST", name, action, nil)
}

func (c *engineClient) TickLocalSchedules(ctx context.Context, dryRun bool) (controllerwire.TickView, error) {
	var result struct {
		Tick controllerwire.TickView `json:"tick"`
	}
	err := c.request(ctx, "POST", "/api/v1/schedules/local/tick", "/api/v1/schedules/local/tick", "", struct {
		DryRun bool `json:"dry_run"`
	}{dryRun}, &result)
	return result.Tick, err
}
