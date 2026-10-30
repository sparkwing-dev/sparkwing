package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	response, err := s.cancel(r.Context(), a, body)
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
		response, err := admitWithFlood(r.Context(), s, func(ctx context.Context) (controllerwire.CreateRunResponse, error) {
			return engine.retry(ctx, a, body)
		})
		if err != nil {
			if refusal := publicQueueDepthRefusal(err); refusal != nil {
				return apierr.WriteHTTP(w, refusal)
			}
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
