package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
)

// RunnerMeteringWriter changes enrolled root metering after rechecking console authority in its transaction.
type RunnerMeteringWriter interface {
	SetRunnerMetered(context.Context, capability.Authorized, string, bool) error
}

// WithRunnerMeteringWriter supplies deployment runner transactions before Handler is constructed.
func (s *Server) WithRunnerMeteringWriter(writer RunnerMeteringWriter) *Server {
	s.runnerMeteringWriter = writer
	return s
}

func (s *Server) handleSetRunnerMetered(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request struct {
		Metered *bool `json:"metered"`
	}
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if request.Metered == nil {
		return invalidEngineBody("metered must be a boolean")
	}
	writer := s.runnerMeteringWriter
	if writer == nil && s.operator != nil {
		writer, _ = any(s.operator).(RunnerMeteringWriter)
	}
	if writer == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	runner := r.PathValue("runner")
	if err := writer.SetRunnerMetered(r.Context(), a, runner, *request.Metered); err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runner": runner, "metered": *request.Metered})
	return nil
}
