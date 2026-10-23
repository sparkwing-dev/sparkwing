package client

import (
	"context"
	"net/url"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

var _ controllerwire.SizingManager = (*engineClient)(nil)

// NewSizingManager reads measurements through the common authenticated client.
func NewSizingManager(options EngineOptions) (controllerwire.SizingManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ListSizing(ctx context.Context, q controllerwire.SizingQuery) (controllerwire.SizingPage, error) {
	var result controllerwire.SizingPage
	if q.Limit < 1 {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "sizing page requires a positive limit"}
	}
	params := url.Values{"limit": {strconv.Itoa(q.Limit)}}
	if q.Pipeline != "" {
		params.Set("pipeline", q.Pipeline)
	}
	if q.Cursor != "" {
		params.Set("cursor", q.Cursor)
	}
	err := c.request(ctx, "GET", "/api/v1/sizing", "/api/v1/sizing?"+params.Encode(), "", nil, &result)
	if err == nil && (len(result.Sizing) > q.Limit || result.Truncated && (result.NextCursor == "" || result.NextCursor == q.Cursor) || !result.Truncated && result.NextCursor != "") {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned inconsistent sizing pagination"}
	}
	return result, err
}

func (c *engineClient) ResetSizing(context.Context, controllerwire.SizingReset) (controllerwire.SizingResetSummary, error) {
	return controllerwire.SizingResetSummary{}, &apierr.Error{Code: apierr.PolicyMissing, Message: "sizing reset has no authorized controller route"}
}
