package controller

import (
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	id := authorized.Target.RunID
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	runs, err := tenant.ListRunRetryTree(r.Context(), id)
	if err != nil {
		return engineLookupError(err)
	}

	runs, metadata, err := pageCollection(r, authorized, runs, func(row *store.Run) string {
		return fmt.Sprintf("%016x/%020d/%s", uint64(row.CreatedAt.UnixNano())^(1<<63), row.Number, row.ID)
	})
	if err != nil {
		return err
	}
	engine := s.engineController
	page := controllerwire.RunPage{Runs: []controllerwire.RunDetail{}, NextCursor: metadata.NextCursor, Truncated: metadata.Truncated}
	for _, run := range runs {
		a := authorized
		a.Target.RunID = run.ID
		if err := s.operator.CheckBinding(r.Context(), a.Principal, a.Access.Binding, a.Target); err != nil {
			return engineLookupError(err)
		}
		detail, err := engine.RunDetail(r.Context(), a)
		if err != nil {
			return err
		}
		page.Runs = append(page.Runs, detail)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
	return nil
}
