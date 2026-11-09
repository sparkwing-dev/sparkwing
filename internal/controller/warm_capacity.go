package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func (s *Server) handleWarmCapacity(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	out, err := s.operatorSettings.WarmCapacity(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleSetWarmCapacity(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var input store.WarmCapacitySettings
	if err := decodeJSON(r, &input); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: "invalid warm capacity settings"}
	}
	if err := input.Validate(); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	writer, err := s.settingsWriter()
	if err != nil {
		return err
	}
	if err := writer.WriteWarmCapacity(r.Context(), a, input); err != nil {
		return operatorSettingsError(err)
	}
	return s.handleWarmCapacity(w, r, a)
}
