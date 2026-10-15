package controller

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithDataObjects supplies the shared physical backend for data bindings and maintenance.
func (s *Server) WithDataObjects(objects blob.ObjectBackend) *Server {
	if s.dataController == nil {
		s.dataController = &DataController{Store: s.store}
	}
	s.dataController.Objects = objects
	if s.store != nil {
		s.store.SetResultBlobRecords(storage.BlobManifests{Objects: objects})
	}
	return s
}

func (s *Server) maintainDataObjects(ctx context.Context, lease store.SingletonLease) error {
	if s.dataController == nil || s.dataController.Objects == nil {
		return nil
	}
	operator := s.store.AsOperator()
	lease, err := operator.RenewSingletonLease(ctx, lease, StorageMaintenanceInterval)
	if err != nil {
		return err
	}
	marked, err := operator.MarkBlobTombstones(ctx, lease, 5000)
	if err != nil {
		return fmt.Errorf("mark blob tombstones: %w", err)
	}
	removed, err := operator.CollectBlobTombstones(ctx, lease, 5000, s.dataController.Objects)
	if err != nil {
		return fmt.Errorf("collect blob tombstones: %w", err)
	}
	if marked > 0 || removed > 0 {
		s.logger.Info("collected unreachable data", "marked", marked, "removed", removed)
	}
	if objects, ok := s.dataController.Objects.(*blob.Filesystem); ok {
		if _, err := operator.RenewSingletonLease(ctx, lease, StorageMaintenanceInterval); err != nil {
			return err
		}
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
