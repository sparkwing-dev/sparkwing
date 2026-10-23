package client

import (
	"context"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// NewOverflowManager uses authenticated transport for the account's Cloud allowance.
func NewOverflowManager(options EngineOptions) (controllerwire.OverflowManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ReadOverflowControl(ctx context.Context) (controllerwire.OverflowControl, error) {
	var result controllerwire.OverflowControl
	err := c.request(ctx, "GET", "/api/v1/overflow", "/api/v1/overflow", "", nil, &result)
	if err == nil {
		err = validateOverflowControl(result)
	}
	return result, err
}

func (c *engineClient) SetOverflowLimit(ctx context.Context, minutes *int64) (controllerwire.OverflowControl, error) {
	var result controllerwire.OverflowControl
	if minutes != nil && (*minutes < 0 || *minutes > math.MaxInt64/60) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "overflow minutes must be a non-negative integer"}
	}
	err := c.request(ctx, "PUT", "/api/v1/overflow", "/api/v1/overflow", "", controllerwire.OverflowControlWrite{MonthlyMinutes: minutes}, &result)
	if err == nil {
		err = validateOverflowControl(result)
	}
	return result, err
}

func validateOverflowControl(result controllerwire.OverflowControl) error {
	period := result.PeriodStart.UTC()
	if result.UsedSeconds < 0 || period.IsZero() || !period.Equal(time.Date(period.Year(), period.Month(), 1, 0, 0, 0, 0, time.UTC)) || result.MonthlyMinutes != nil && (*result.MonthlyMinutes < 0 || *result.MonthlyMinutes > math.MaxInt64/60) {
		return &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid overflow allowance or usage period"}
	}
	return nil
}
