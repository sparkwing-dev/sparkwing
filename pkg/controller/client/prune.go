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

func (c *engineClient) DeleteRun(ctx context.Context, run string) error {
	if !validSelector(run) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	return c.request(ctx, "DELETE", "/api/v1/runs/{run}", "/api/v1/runs/"+url.PathEscape(run), "", nil, nil)
}
