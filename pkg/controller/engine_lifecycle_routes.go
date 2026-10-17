package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (s *EngineController) handleTransition(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.JobTransitionRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.TransitionJob(r.Context(), a, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *EngineController) handleFinish(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.FinishRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.Finish(r.Context(), a, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *EngineController) handleCancel(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.CancelRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.Cancel(r.Context(), a, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *Server) handleRetry(engine *EngineController) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		var body controllerwire.RetryRequest
		if err := decodeEngineRequest(r, &body); err != nil {
			return err
		}
		if err := body.Validate(); err != nil {
			return err
		}
		if err := s.admissionFloodRefusal(r.Context(), a); err != nil {
			return err
		}
		response, err := engine.Retry(r.Context(), a, body)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, response)
		return nil
	}
}

func (s *EngineController) handleRenewal(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.RenewalRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.Renew(r.Context(), a, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}
