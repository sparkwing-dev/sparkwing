package client

import (
	"context"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.RunPruner = (*engineClient)(nil)

// NewRunPruner selects and deletes runs through the shared controller transport.
func NewRunPruner(options EngineOptions) (controllerwire.RunPruner, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *engineClient) DeleteRun(ctx context.Context, run string) (controllerwire.RunDeletion, error) {
	var result controllerwire.RunDeletion
	if !validSelector(run) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	err := c.request(ctx, "DELETE", "/api/v1/runs/{run}", "/api/v1/runs/"+url.PathEscape(run), "", nil, &result)
	if err == nil && result.RunID == "" {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller deletion is missing its run identity"}
	}
	return result, err
}
