package controller

import (
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type reconcileOrphansReq struct {
	ThresholdNanos int64 `json:"threshold_nanos,omitempty"`
}

type reconcileOrphansResp struct {
	Reconciled int `json:"reconciled"`
}

func (s *Server) handleReconcileOrphans(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	var body reconcileOrphansReq
	if err := decodeOptionalJSON(r, &body); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	// safety: the store reads a zero threshold as "everything running is
	// orphaned", so the caller names the age rather than inheriting one here.
	if body.ThresholdNanos <= 0 {
		return &apierr.Error{Code: apierr.BadRequest, Message: "threshold_nanos must be > 0"}
	}
	n, err := store.Maintenance.ReconcileOrphanedLocalRuns(s.store, r.Context(), time.Duration(body.ThresholdNanos))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, reconcileOrphansResp{Reconciled: n})
	return nil
}
