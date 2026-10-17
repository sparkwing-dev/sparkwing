package controller

import (
	"context"
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CancelJob records cancellation intent while preserving held jobs and their result rights.
func (s *EngineController) CancelJob(ctx context.Context, a capability.Authorized, body controllerwire.CancelRequest) (controllerwire.Applied, error) {
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	actor := engineActor(a)
	if a.Access.Credential == capability.CredentialMember {
		actor.Kind = store.ActorOperator
	}
	applied, err := tenant.TransitionNode(ctx, store.NodeTransition{
		Ref: store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID},
		To:  store.NodeCancelled, Cause: store.CauseCancel, Actor: actor,
		Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: body.Reason}},
	})
	return appliedResponse(applied), engineLookupError(err)
}

// ReadFailure checks the declared recovery edge and immutable source generation
// transactionally. An absent recorded failure remains absent.
func (s *EngineController) ReadFailure(ctx context.Context, a capability.Authorized) (*nodeproto.JobFailure, error) {
	if a.Target.SourceGeneration <= 0 {
		return nil, invalidEngineBody("generation must be positive")
	}
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return nil, err
	}
	failure, err := tenant.ReadFailure(ctx, store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}, a.Target.SourceGeneration, engineActor(a))
	return failure, engineLookupError(err)
}

func (s *EngineController) failureTarget(r *http.Request) (capability.RequestEvidence, error) {
	evidence, _ := runSelectorTarget(r)
	query := r.URL.Query()["generation"]
	if len(query) != 1 {
		return evidence, invalidEngineBody("one generation is required")
	}
	generation, err := strconv.ParseInt(query[0], 10, 64)
	evidence.Target.SourceGeneration = generation
	if err != nil || evidence.Target.SourceGeneration <= 0 {
		return evidence, invalidEngineBody("generation must be positive")
	}
	return evidence, nil
}

func (s *EngineController) handleCancelJob(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.CancelRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.CancelJob(r.Context(), a, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *EngineController) handleFailure(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	failure, err := s.ReadFailure(r.Context(), a)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, failure)
	return nil
}
