package controller

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (s *Server) handleFailedGitHubDeliveries(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.githubAppWriter(r, a)
	if err != nil {
		return err
	}
	deliveries, err := writer.ListFailedGitHubDeliveries(r.Context(), a)
	if err != nil {
		return accountMutationError(err)
	}
	writeJSON(w, http.StatusOK, deliveries)
	return nil
}

func (s *Server) handleProcessGitHubDeliveryAgain(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.githubAppWriter(r, a)
	if err != nil {
		return err
	}
	if err := writer.ProcessGitHubDeliveryAgain(r.Context(), a, r.PathValue("delivery")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &apierr.Error{Code: apierr.NotFound, Message: "failed delivery not found"}
		}
		return accountMutationError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
