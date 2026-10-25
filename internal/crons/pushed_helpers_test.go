package crons

import (
	"context"
	"errors"
)

// disarmRepoURL deletes every schedule pushed for one repository URL, and their
// histories, returning how many rows went.
func (s *Service) disarmRepoURL(ctx context.Context, repoURL string) (int, error) {
	if repoURL == "" {
		return 0, errors.New("crons: a repository URL is required to disarm pushed schedules")
	}
	return s.Schedules.DeleteCronSchedulesForRepo(ctx, repoURL)
}
