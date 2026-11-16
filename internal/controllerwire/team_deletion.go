package controllerwire

import "context"

// TeamDeleter asks the controller to delete the authenticated team.
type TeamDeleter interface {
	DeleteTeam(ctx context.Context, confirmSlug string) (TeamDeletion, error)
}

// TeamDeletionRequest repeats the team's slug so a token for another team
// cannot delete this one by mistake.
type TeamDeletionRequest struct {
	ConfirmSlug string `json:"confirm_slug"`
}

// TeamDeletion is the controller's record of one team's deletion. State is
// pending until the controller's purge removes the team's data, then done.
type TeamDeletion struct {
	Slug        string `json:"slug"`
	State       string `json:"state"`
	RequestedAt int64  `json:"requested_at"`
	FinishedAt  *int64 `json:"finished_at"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"last_error"`
}
