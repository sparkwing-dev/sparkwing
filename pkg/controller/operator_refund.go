package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (s *Server) handleOperatorRefund(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req controllerwire.OperatorRefund
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	writer, err := s.consoleWriter()
	if err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "Idempotency-Key is required"}
	}
	result, err := writer.ConsoleRefund(r.Context(), a, req, key)
	if err != nil {
		return engineLookupError(err)
	}
	response := controllerwire.OperatorRefundRecorded{Team: string(result.Team), PaidMicro: result.PaidMicro, ReversedMicro: result.ReversedMicro, BalanceMicro: result.BalanceMicro, Created: result.Created}
	if result.Grant != nil {
		response.GrantID = result.Grant.ID
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}
