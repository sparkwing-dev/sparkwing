package client

import (
	"context"

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
