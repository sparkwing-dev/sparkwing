package client

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (c *engineClient) ReadStanding(ctx context.Context) (controllerwire.TeamStanding, error) {
	var billing struct {
		Standing *controllerwire.TeamStanding `json:"standing"`
	}
	if err := c.request(ctx, "GET", "/api/v1/team/billing", "/api/v1/team/billing", "", nil, &billing); err != nil {
		return controllerwire.TeamStanding{}, err
	}
	if billing.Standing == nil || billing.Standing.Tier == "" {
		return controllerwire.TeamStanding{}, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned no team standing"}
	}
	return *billing.Standing, nil
}

func (c *engineClient) RequestStanding(ctx context.Context, req controllerwire.StandingRequestWrite) (controllerwire.StandingRequest, error) {
	var made controllerwire.StandingRequest
	err := c.request(ctx, "POST", "/api/v1/team/billing/standing-request", "/api/v1/team/billing/standing-request", "", req, &made)
	if err == nil && (made.ID == "" || made.State == "") {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an incomplete standing request"}
	}
	return made, err
}
