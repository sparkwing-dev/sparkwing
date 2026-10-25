package crons

import (
	"context"
	"fmt"
	"path/filepath"
)

// disarmRepo deletes every schedule of one repository checkout, their
// histories and their pinned binaries, returning how many rows went.
func (s *Service) disarmRepo(ctx context.Context, repoRoot string) (int, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", repoRoot, err)
	}
	rows, err := s.Schedules.ListCronSchedules(ctx)
	if err != nil {
		return 0, err
	}
	removed, err := s.Schedules.DeleteCronSchedulesForRepo(ctx, root)
	if err != nil {
		return 0, err
	}
	for _, sched := range rows {
		if sched.RepoPath != root {
			continue
		}
		if perr := s.removePin(sched.ID); perr != nil {
			return removed, perr
		}
	}
	return removed, nil
}
