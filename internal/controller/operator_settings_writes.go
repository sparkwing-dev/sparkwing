package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// OperatorSettingsWriter rechecks current deployment authority in each committing transaction.
type OperatorSettingsWriter interface {
	WriteComputeLimits(context.Context, capability.Authorized, map[string]int64) (store.ComputeLimits, error)
	WriteSignUpSettings(context.Context, capability.Authorized, store.SignUpSettingsChange) error
	WriteStorageSettings(context.Context, capability.Authorized, store.StorageSettings) error
	WriteStorageQuota(context.Context, capability.Authorized, store.StorageQuota) (store.StorageQuota, error)
	WriteStorageAllowance(context.Context, capability.Authorized, int64) (store.StorageQuota, error)
}

// WithOperatorSettingsWriter supplies the deployment settings transactions.
func (s *Server) WithOperatorSettingsWriter(writer OperatorSettingsWriter) *Server {
	s.operatorSettingsWrites = writer
	return s
}

func (s *Server) settingsWriter() (OperatorSettingsWriter, error) {
	if s.operatorSettingsWrites != nil {
		return s.operatorSettingsWrites, nil
	}
	if s.store != nil {
		return s.operator, nil
	}
	return nil, missingEndpointPolicy()
}
