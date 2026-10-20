package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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

	engine := s.engineController
	page := controllerwire.RunPage{Runs: []controllerwire.RunDetail{}}
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
