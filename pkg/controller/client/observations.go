package client

import (
	"context"
	"net/url"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.RunReader = (*engineClient)(nil)

func (c *engineClient) ListRuns(ctx context.Context, query controllerwire.RunQuery) (controllerwire.RunPage, error) {
	var result controllerwire.RunPage
	if query.Limit < 1 || query.ParentRunID != "" && !validSelector(query.ParentRunID) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "run page requires a positive limit and valid parent selector"}
	}
	params := url.Values{"limit": {strconv.Itoa(query.Limit)}}
	if query.ParentRunID != "" {
		params.Set("parent_run_id", query.ParentRunID)
	}
	if query.Cursor != "" {
		params.Set("cursor", query.Cursor)
	}
	err := c.request(ctx, "GET", "/api/v1/runs", "/api/v1/runs?"+params.Encode(), "", nil, &result)
	if err == nil && (result.Truncated && (result.NextCursor == "" || result.NextCursor == query.Cursor) || !result.Truncated && result.NextCursor != "") {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned inconsistent run pagination"}
	}
	return result, err
}

func (c *engineClient) ReadSteps(ctx context.Context, run string) (controllerwire.RunSteps, error) {
	var result controllerwire.RunSteps
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "GET", "/api/v1/runs/{run}/steps", "/api/v1/runs/"+url.PathEscape(run)+"/steps", "", nil, &result)
	return result, err
}

func (c *engineClient) ReadReceipt(ctx context.Context, run string) (controllerwire.Receipt, error) {
	var result controllerwire.Receipt
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "GET", "/api/v1/runs/{run}/receipt", "/api/v1/runs/"+url.PathEscape(run)+"/receipt", "", nil, &result)
	return result, err
}
