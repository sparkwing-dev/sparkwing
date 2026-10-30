package client

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.RunReader = (*engineClient)(nil)

func (c *engineClient) ListRuns(ctx context.Context, query controllerwire.RunQuery) (controllerwire.RunPage, error) {
	var result controllerwire.RunPage
	params, err := runQueryParams(query)
	if err != nil {
		return result, err
	}
	err = c.request(ctx, "GET", "/api/v1/runs", "/api/v1/runs?"+params.Encode(), "", nil, &result)
	if err == nil && (result.Truncated && (result.NextCursor == "" || result.NextCursor == query.Cursor) || !result.Truncated && result.NextCursor != "") {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned inconsistent run pagination"}
	}
	return result, err
}

func runQueryParams(query controllerwire.RunQuery) (url.Values, error) {
	if query.Limit < 1 || query.AfterNumber < 0 || query.ParentRunID != "" && !validSelector(query.ParentRunID) {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "run page requires a positive limit, nonnegative run number and valid parent selector"}
	}
	if !query.StartedBefore.IsZero() && query.StartedAfter.After(query.StartedBefore) || !query.FinishedBefore.IsZero() && query.FinishedAfter.After(query.FinishedBefore) {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "run query has an inverted date range"}
	}
	params := url.Values{"limit": {strconv.Itoa(query.Limit)}}
	for key, value := range map[string]string{
		"parent_run_id": query.ParentRunID, "cursor": query.Cursor, "pipeline": query.Pipeline,
		"status": query.Status, "sha": query.SHA, "error": query.Error, "search": query.Search,
	} {
		if value != "" {
			params.Set(key, value)
		}
	}
	for _, branch := range query.Branches {
		params.Add("branch", branch)
	}
	for _, repo := range query.Repos {
		params.Add("repo", repo)
	}
	if query.RootOnly {
		params.Set("root_only", "true")
	}
	if query.AfterNumber > 0 {
		params.Set("after_number", strconv.FormatInt(query.AfterNumber, 10))
	}
	for key, value := range map[string]time.Time{
		"started_after": query.StartedAfter, "started_before": query.StartedBefore,
		"finished_after": query.FinishedAfter, "finished_before": query.FinishedBefore,
	} {
		if !value.IsZero() {
			params.Set(key, value.UTC().Format(time.RFC3339Nano))
		}
	}
	return params, nil
}

func (c *engineClient) ReadSteps(ctx context.Context, run string) (controllerwire.RunSteps, error) {
	var result controllerwire.RunSteps
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.requestList(ctx, "/api/v1/runs/{run}/steps", "/api/v1/runs/"+url.PathEscape(run)+"/steps", &result, "steps")
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
