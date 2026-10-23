package client

import (
	"context"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

var _ controllerwire.RunPruner = (*engineClient)(nil)

// NewRunPruner selects and deletes runs through the shared controller transport.
func NewRunPruner(options EngineOptions) (controllerwire.RunPruner, error) {
	return newEngineClient(options)
}

func (c *engineClient) DeleteRun(ctx context.Context, run string) error {
	if !validSelector(run) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
	}
	return c.request(ctx, "DELETE", "/api/v1/runs/{run}", "/api/v1/runs/"+url.PathEscape(run), "", nil, nil)
}
