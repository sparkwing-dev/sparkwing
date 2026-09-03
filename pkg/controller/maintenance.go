package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type reconcileOrphansReq struct {
	ThresholdNanos int64 `json:"threshold_nanos,omitempty"`
}

type reconcileOrphansResp struct {
	Reconciled int `json:"reconciled"`
}

func (s *Server) handleReconcileOrphans(w http.ResponseWriter, r *http.Request) {
	var body reconcileOrphansReq
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	// safety: the store reads a zero threshold as "everything running is
	// orphaned", so the caller names the age rather than inheriting one here.
	if body.ThresholdNanos <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("threshold_nanos must be > 0"))
		return
	}
	n, err := store.Maintenance.ReconcileOrphanedLocalRuns(s.store, r.Context(), time.Duration(body.ThresholdNanos))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, reconcileOrphansResp{Reconciled: n})
}
