package controller

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// WithDataObjects supplies the shared physical backend for data bindings and maintenance.
func (s *Server) WithDataObjects(objects blob.ObjectBackend) *Server {
	if s.dataController == nil {
		s.dataController = &DataController{}
	}
	s.dataController.Objects = objects
	s.configureDataStore(s.dataController)
	return s
}

func (s *Server) maintainDataObjects(ctx context.Context) error {
	if s.dataController == nil || s.dataController.Objects == nil {
		return nil
	}
	operator := s.operator
	if err := operator.ExpireSourceUploads(ctx, 5000, s.dataController.Objects); err != nil {
		return fmt.Errorf("expire source uploads: %w", err)
	}
	marked, err := operator.MarkBlobTombstones(ctx, 5000)
	if err != nil {
		return fmt.Errorf("mark blob tombstones: %w", err)
	}
	removed, err := operator.CollectBlobTombstones(ctx, 5000, s.dataController.Objects)
	if err != nil {
		return fmt.Errorf("collect blob tombstones: %w", err)
	}
	if err := operator.DeleteExpiredBlobStaging(ctx, 5000, s.dataController.Objects); err != nil {
		return fmt.Errorf("delete expired blob staging: %w", err)
	}
	if marked > 0 || removed > 0 {
		s.logger.Info("collected unreachable data", "marked", marked, "removed", removed)
	}
	if objects, ok := s.dataController.Objects.(*blob.Filesystem); ok {
		if _, err := objects.ExpireLogs(ctx); err != nil {
			return fmt.Errorf("expire filesystem logs: %w", err)
		}
	}
	return nil
}

func (s *Server) deleteDataLogs(ctx context.Context, team store.Team, run string) (int64, error) {
	if s.dataController == nil || s.dataController.Objects == nil {
		return 0, nil
	}
	objects, ok := s.dataController.Objects.(blob.LogDeleter)
	if !ok {
		return 0, fmt.Errorf("data backend cannot delete logs")
	}
	return objects.DeleteLogs(ctx, string(team), run)
}
