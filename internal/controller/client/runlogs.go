package client

import (
	"context"
	"net/url"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.LogReader = (*engineClient)(nil)

func NewLogReader(options EngineOptions) (controllerwire.LogReader, error) {
	return newEngineClient(options)
}

func (c *engineClient) ReadLogs(ctx context.Context, run string, q controllerwire.LogQuery) (controllerwire.LogPage, error) {
	var result controllerwire.LogPage
	if !validSelector(run) || q.Job != "" && !validSelector(q.Job) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run or job selector"}
	}
	if err := q.Validate(); err != nil {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	pattern, path := "/api/v1/runs/{run}/logs", "/api/v1/runs/"+url.PathEscape(run)+"/logs"
	params := logQueryParams(q)
	if q.Grep != "" {
		pattern, path = pattern+"/search", path+"/search"
		if q.Job != "" {
			params.Set("job", q.Job)
		}
	} else if q.Job != "" && !q.Tree {
		pattern = "/api/v1/runs/{run}/jobs/{job}/logs"
		path = "/api/v1/runs/" + url.PathEscape(run) + "/jobs/" + url.PathEscape(q.Job) + "/logs"
	} else if q.Job != "" {
		params.Set("job", q.Job)
	}
	if len(params) != 0 {
		path += "?" + params.Encode()
	}
	err := c.request(ctx, "GET", pattern, path, "", nil, &result)
	if err == nil {
		if invalid := result.Validate(q.Cursor); invalid != nil {
			err = &apierr.Error{Code: apierr.ProtocolError, Message: invalid.Error()}
		}
	}
	return result, err
}

func logQueryParams(q controllerwire.LogQuery) url.Values {
	params := url.Values{}
	for name, value := range map[string]*int{"tail": q.Tail, "head": q.Head} {
		if value != nil {
			params.Set(name, strconv.Itoa(*value))
		}
	}
	for name, value := range map[string]string{"lines": q.Lines, "grep": q.Grep, "cursor": q.Cursor} {
		if value != "" {
			params.Set(name, value)
		}
	}
	if q.Since != 0 {
		params.Set("since", q.Since.String())
	}
	for name, value := range map[string]bool{"tree": q.Tree, "events_only": q.EventsOnly, "no_events": q.NoEvents} {
		if value {
			params.Set(name, "true")
		}
	}
	return params
}

func (c *engineClient) SearchLogs(ctx context.Context, q controllerwire.LogSearchQuery) (controllerwire.LogSearchPage, error) {
	var result controllerwire.LogSearchPage
	if q.Pattern == "" || q.MaxMatches < 0 || q.Job != "" && !validSelector(q.Job) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "log search requires a pattern, a valid job selector and nonnegative max_matches"}
	}
	params, err := runQueryParams(q.Runs)
	if err != nil {
		return result, err
	}
	params.Set("grep", q.Pattern)
	params.Set("max_matches", strconv.Itoa(q.MaxMatches))
	if q.Job != "" {
		params.Set("job", q.Job)
	}
	err = c.request(ctx, "GET", "/api/v1/runs/grep", "/api/v1/runs/grep?"+params.Encode(), "", nil, &result)
	if err == nil {
		if invalid := result.Validate(q.Runs.Cursor); invalid != nil {
			err = &apierr.Error{Code: apierr.ProtocolError, Message: invalid.Error()}
		}
	}
	return result, err
}
