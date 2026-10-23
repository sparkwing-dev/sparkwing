package client

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// NewBillingManager uses authenticated transport for the team's standing and automatic refill.
func NewBillingManager(options EngineOptions) (controllerwire.BillingManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ReadRefill(ctx context.Context) (controllerwire.TeamRefill, error) {
	var billing struct {
		Refill *controllerwire.TeamRefill `json:"refill"`
	}
	if err := c.request(ctx, "GET", "/api/v1/team/billing", "/api/v1/team/billing", "", nil, &billing); err != nil {
		return controllerwire.TeamRefill{}, err
	}
	if billing.Refill == nil {
		return controllerwire.TeamRefill{}, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned no refill settings"}
	}
	return *billing.Refill, nil
}

func (c *engineClient) SetRefill(ctx context.Context, w controllerwire.RefillWrite) error {
	return c.request(ctx, "PUT", "/api/v1/team/billing/refill", "/api/v1/team/billing/refill", "", w, nil)
}
