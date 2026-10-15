package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// OperatorSettingsWriter rechecks current deployment authority in each committing transaction.
type OperatorSettingsWriter interface {
	WriteComputeLimits(context.Context, capability.Authorized, map[string]int64) (store.ComputeLimits, error)
	WriteSignUpSettings(context.Context, capability.Authorized, SignUpSettingsChange) error
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
		if writer, ok := any(s.store.AsOperator()).(OperatorSettingsWriter); ok {
			return writer, nil
		}
	}
	return nil, missingEndpointPolicy()
}
