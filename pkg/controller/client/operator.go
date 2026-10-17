package client

import (
	"context"
	"net/url"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.OperatorManager = (*engineClient)(nil)

// NewOperatorManager uses the common transport and deployment route policy.
func NewOperatorManager(options EngineOptions) (controllerwire.OperatorManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) CreateOperatorTeam(ctx context.Context, input controllerwire.OperatorTeamCreate) (controllerwire.OperatorTeamCreated, error) {
	var out controllerwire.OperatorTeamCreated
	err := c.request(ctx, "POST", "/api/v1/operator/teams", "/api/v1/operator/teams", "", input, &out)
	if err == nil && (out.Team != input.Slug || out.AccountID == "" || out.Capability == "" || out.Prefix == "" || !slices.Contains(out.Grants, capability.TeamAdmin)) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned incomplete team administration"}
	}
	return out, err
}

func (c *engineClient) ReadOperatorLimits(ctx context.Context) (controllerwire.OperatorLimits, error) {
	var out controllerwire.OperatorLimits
	err := c.request(ctx, "GET", "/api/v1/operator/limits", "/api/v1/operator/limits", "", nil, &out)
	if err == nil && out.Limits == nil {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned no operational guards"}
	}
	return out, err
}

func (c *engineClient) WriteOperatorLimits(ctx context.Context, input controllerwire.OperatorLimitsWrite) (controllerwire.OperatorLimits, error) {
	var out controllerwire.OperatorLimits
	if err := input.Validate(); err != nil {
		return out, &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	err := c.request(ctx, "PUT", "/api/v1/operator/limits", "/api/v1/operator/limits", "", input, &out)
	if err == nil {
		for name, value := range input.Limits {
			if got, exists := out.Limits[name]; !exists || got != value {
				return out, &apierr.Error{Code: apierr.ProtocolError, Message: "controller did not confirm the requested operational guard"}
			}
		}
	}
	return out, err
}

func (c *engineClient) ReadObjectStoreBreaker(ctx context.Context) (controllerwire.ObjectStoreBreaker, error) {
	var out controllerwire.ObjectStoreBreaker
	err := c.request(ctx, "GET", "/api/v1/operator/object-store/breaker", "/api/v1/operator/object-store/breaker", "", nil, &out)
	return out, err
}

func (c *engineClient) ResetObjectStoreBreaker(ctx context.Context, key string) (controllerwire.ObjectStoreBreaker, error) {
	var out controllerwire.ObjectStoreBreaker
	if key == "" {
		return out, &apierr.Error{Code: apierr.InvalidArgs, Message: "breaker reset requires an idempotency key"}
	}
	err := c.request(ctx, "POST", "/api/v1/operator/object-store/reset-breaker", "/api/v1/operator/object-store/reset-breaker", key, nil, &out)
	return out, err
}

func (c *engineClient) ReadOperatorTeam(ctx context.Context, team string) (controllerwire.OperatorTeam, error) {
	var out controllerwire.OperatorTeam
	err := c.request(ctx, "GET", "/api/v1/operator/teams/{team}", "/api/v1/operator/teams/"+url.PathEscape(team), "", nil, &out)
	if err == nil && out.Team != team {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned a different team"}
	}
	return out, err
}
