package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// QueuePriorityWriter rechecks tenant administration and commits an idempotent queue rank.
type QueuePriorityWriter interface {
	Team() store.Team
	SetAuthorizedGroupPriority(context.Context, capability.Authorized, string, controllerwire.QueuePriorityWrite) (controllerwire.QueuePriorityResult, error)
}

// WithQueuePriorityWriter supplies the tenant queue ordering transaction.
func (s *Server) WithQueuePriorityWriter(resolve func(context.Context, store.Team) (QueuePriorityWriter, error)) *Server {
	s.resolveQueuePriorityWriter = resolve
	return s
}

func (s *Server) handleQueuePriority(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	group := r.PathValue("group")
	if err := controllerwire.ValidateQueueGroupName(group); err != nil {
		return invalidEngineBody(err.Error())
	}
	var input controllerwire.QueuePriorityWrite
	if err := decodeEngineRequest(r, &input); err != nil {
		return err
	}
	if err := input.Validate(); err != nil {
		return invalidEngineBody(err.Error())
	}
	key, err := controlIdempotencyKey(r)
	if err != nil {
		return err
	}
	input.IdempotencyKey = key
	var writer QueuePriorityWriter
	if s.resolveQueuePriorityWriter != nil {
		writer, err = s.resolveQueuePriorityWriter(r.Context(), store.Team(a.Target.Team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
		writer, _ = any(tenant).(QueuePriorityWriter)
	}
	if err != nil {
		return engineLookupError(err)
	}
	if writer == nil || a.Target.Team == "" || writer.Team() != store.Team(a.Target.Team) {
		return missingEndpointPolicy()
	}
	result, err := writer.SetAuthorizedGroupPriority(r.Context(), a, group, input)
	if err != nil {
		return engineLookupError(err)
	}
	if result.Group != group || result.RunID == "" || result.Position < 0 || result.PreviousPosition < 0 || result.Participants < 0 || result.Found && input.Priority != nil && result.Priority != *input.Priority {
		return dataProjectionError()
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
