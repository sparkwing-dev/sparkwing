package client

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// NewTeamDeleter uses authenticated transport to delete the credential's team.
func NewTeamDeleter(options EngineOptions) (controllerwire.TeamDeleter, error) {
	return newEngineClient(options)
}

func (c *engineClient) DeleteTeam(ctx context.Context, confirmSlug string) (controllerwire.TeamDeletion, error) {
	var result controllerwire.TeamDeletion
	if confirmSlug == "" {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "team deletion requires the team's slug as confirmation"}
	}
	err := c.request(ctx, "DELETE", "/api/v1/team", "/api/v1/team", "", controllerwire.TeamDeletionRequest{ConfirmSlug: confirmSlug}, &result)
	if err == nil && (result.Slug == "" || result.State == "") {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid team deletion"}
	}
	return result, err
}
