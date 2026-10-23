package client

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// CheckIdentity authenticates a connection without requiring a team grant.
func CheckIdentity(ctx context.Context, options EngineOptions) error {
	c, err := newEngineClient(options)
	if err != nil {
		return err
	}
	var result struct {
		Principal string                 `json:"principal"`
		Kind      capability.SubjectKind `json:"kind"`
	}
	if err := c.request(ctx, "GET", "/api/v1/auth/whoami", "/api/v1/auth/whoami", "", nil, &result); err != nil {
		return err
	}
	if result.Principal == "" || result.Kind == "" {
		return &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned incomplete identity"}
	}
	return nil
}
