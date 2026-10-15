package controller

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithDataObjects supplies the shared physical backend for data bindings and maintenance.
func (s *Server) WithDataObjects(objects blob.ObjectBackend) *Server {
	s.data = &DataController{Store: s.store, Objects: objects}
	return s
}

func (s *Server) maintainDataObjects(ctx context.Context, lease store.SingletonLease) error {
	if s.data == nil || s.data.Objects == nil {
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
	removed, err := operator.CollectBlobTombstones(ctx, lease, 5000, s.data.Objects)
	if err != nil {
		return fmt.Errorf("collect blob tombstones: %w", err)
	}
	if marked > 0 || removed > 0 {
		s.logger.Info("collected unreachable data", "marked", marked, "removed", removed)
	}
	if objects, ok := s.data.Objects.(*blob.Filesystem); ok {
		if _, err := operator.RenewSingletonLease(ctx, lease, StorageMaintenanceInterval); err != nil {
			return err
		}
		if _, err := objects.ExpireLogs(ctx); err != nil {
			return fmt.Errorf("expire filesystem logs: %w", err)
		}
	}
	return nil
}
