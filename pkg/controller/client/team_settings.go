package client

import (
	"context"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// TeamSettingsClient reads and changes the selected team's enrollment policy and claim ranking.
type TeamSettingsClient struct{ client *engineClient }

// NewTeamSettingsClient uses the selected controller's authenticated team settings routes.
func NewTeamSettingsClient(options EngineOptions) (*TeamSettingsClient, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	return &TeamSettingsClient{client: c}, nil
}

// ReadTeamSettings reads the authenticated team's settings.
func (c *TeamSettingsClient) ReadTeamSettings(ctx context.Context) (controllerwire.TeamSettings, error) {
	var result controllerwire.TeamSettings
	err := c.client.request(ctx, "GET", "/api/v1/team/settings", "/api/v1/team/settings", "", nil, &result)
	return result, err
}

// SetTeamSettings changes the named settings under current team administration.
func (c *TeamSettingsClient) SetTeamSettings(ctx context.Context, settings controllerwire.TeamSettingsUpdate) (controllerwire.TeamSettings, error) {
	var result controllerwire.TeamSettings
	err := c.client.request(ctx, "PUT", "/api/v1/team/settings", "/api/v1/team/settings", "", settings, &result)
	return result, err
}

// RequestMachineAutomation requests or withdraws automated runs for the caller's personal machine.
func (c *TeamSettingsClient) RequestMachineAutomation(ctx context.Context, runner string, requested bool) (controllerwire.MachineAutomation, error) {
	var result controllerwire.MachineAutomation
	err := c.client.request(ctx, "PUT", "/api/v1/runners/{runner}/automation", "/api/v1/runners/"+url.PathEscape(runner)+"/automation", "", controllerwire.MachineAutomationRequest{Requested: &requested}, &result)
	return result, err
}

// ApproveMachineAutomation approves or revokes a personal machine's automated runs as a team administrator.
func (c *TeamSettingsClient) ApproveMachineAutomation(ctx context.Context, runner string, approved bool) (controllerwire.MachineAutomation, error) {
	var result controllerwire.MachineAutomation
	err := c.client.request(ctx, "PUT", "/api/v1/runners/{runner}/automation/approval", "/api/v1/runners/"+url.PathEscape(runner)+"/automation/approval", "", controllerwire.MachineAutomationApproval{Approved: &approved}, &result)
	return result, err
}
