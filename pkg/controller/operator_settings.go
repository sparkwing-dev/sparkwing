package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type operatorSettingsStore interface {
	ComputeLimits(context.Context) (store.ComputeLimits, error)
	SetComputeLimits(context.Context, map[string]int64) (store.ComputeLimits, error)
	ComputeUsage(context.Context) (store.ComputeUsage, error)
	SignUpGate(context.Context) (store.SignUpGate, error)
	SignUpCounts(context.Context, time.Time) (store.SignUpCounts, error)
	SetSignUpLimits(context.Context, store.SignUpLimits) (store.SignUpGate, error)
	SetSignUpMode(context.Context, store.SignUpMode, string, string, time.Time) (store.SignUpGate, error)
	StorageSettings(context.Context) (store.StorageSettings, error)
	SetStorageSettings(context.Context, store.StorageSettings) error
	ListStorageQuotas(context.Context) ([]store.StorageQuota, error)
	TopStorageTeams(context.Context, string, int) ([]store.StorageTeamUsage, error)
	SetStorageQuota(context.Context, store.StorageQuota) error
	StorageQuotaFor(context.Context, string) (store.StorageQuota, error)
	SetStorageAllowance(context.Context, string, int64) (store.StorageQuota, error)
}

func (s *Server) handleOperatorObjectStore(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	settings, err := s.operatorSettings.StorageSettings(r.Context())
	if err != nil {
		return err
	}
	size, _, alarm, sampled := s.storage.read()
	out := storageStateJSON{Settings: storageSettingsToJSON(settings), Alarm: alarm}
	if sampled {
		out.Database = &size
	}
	if err := s.appendAdminStorage(r.Context(), &out, store.StorageMonth(time.Now().UTC())); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

var _ operatorSettingsStore = (*store.Store)(nil)

func operatorSettingsError(err error) error {
	if errors.Is(err, store.ErrInvalidInput) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid operator settings"}
	}
	return err
}
