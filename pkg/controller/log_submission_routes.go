package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type logSubmissionObjects interface {
	logseg.LogPutSigner
	logseg.SegmentObjects
	LogClassReady(context.Context, string) error
}

func (s *Server) logSubmissionEndpoints(e *EngineController) []Endpoint {
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/logs", Decode: e.runTarget, Handle: s.handleLiveLogSubmission},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/log-segments", Decode: e.runTarget, Handle: s.handleLogSigning},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/log-manifest", Decode: e.runTarget, Handle: s.handleLogCommit},
		{Method: "POST", Pattern: "/api/v1/claims/{claim}/logs", Decode: claimIDTarget, ResolveTarget: ownUnitTarget, Handle: s.handleLiveLogSubmission},
		{Method: "POST", Pattern: "/api/v1/claims/{claim}/log-segments", Decode: claimIDTarget, ResolveTarget: ownUnitTarget, Handle: s.handleLogSigning},
		{Method: "POST", Pattern: "/api/v1/claims/{claim}/log-manifest", Decode: claimIDTarget, ResolveTarget: ownUnitTarget, Handle: s.handleLogCommit},
	}
}

func (s *Server) submissionObjects() (logSubmissionObjects, error) {
	if s.dataController != nil {
		if objects, ok := s.dataController.Objects.(logSubmissionObjects); ok {
			return objects, nil
		}
	}
	return nil, &apierr.Error{Code: apierr.Unavailable, Message: "log storage is not configured"}
}

func (s *Server) logSubmission(r *http.Request, a capability.Authorized, proposed blob.LogSubject) (*store.Tenant, blob.LogSubject, error) {
	subject := blob.LogSubject{Team: a.Target.Team, RunID: a.Target.RunID}
	if r.PathValue("job") != "" {
		subject.Kind = blob.LogJobAttempt
		e := s.engineController
		if e == nil {
			e = &EngineController{Store: s.store}
		}
		tenant, err := e.runTransactions(r.Context(), a.Target.Team)
		if err != nil {
			return nil, subject, err
		}
		attempt, err := reportingAttempt(r.Context(), tenant, a)
		if err != nil {
			return nil, subject, err
		}
		subject.Attempt = &attempt
	} else {
		c := a.Principal.Capability
		if c.Unit != capability.UnitTrigger && c.Unit != capability.UnitPlan && c.Unit != capability.UnitEval {
			return nil, subject, invalidEngineBody("claim logs require an owner unit")
		}
		subject.Kind = blob.LogOwnerUnit
		subject.Unit = &blob.LogUnit{Kind: blob.LogUnitKind(c.Unit), Generation: a.Target.UnitGeneration}
		if c.Unit == capability.UnitEval {
			subject.Unit.Source = &blob.LogSource{NodeID: a.Target.UnitSourceNodeID, Generation: a.Target.UnitSourceGeneration}
		}
	}
	if err := subject.Validate(); err != nil || !reflect.DeepEqual(subject, proposed) {
		return nil, subject, invalidEngineBody("log subject differs from authorized unit")
	}
	tenant, err := s.reportingTenant(r, a)
	return tenant, subject, err
}

func logActor(a capability.Authorized, subject blob.LogSubject) store.Actor {
	actor := engineActor(a)
	if subject.Unit != nil && subject.Unit.Kind == blob.LogEval {
		actor.Generation = subject.Unit.Generation
	}
	return actor
}

func (s *Server) handleLogSigning(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body blob.LogSigningRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.FirstSegment < 1 || body.FirstSegment > int64(limits.MaxLogSegmentNumber) || body.Count < 1 || body.Count > limits.LogUploadBatch || int64(body.Count)-1 > int64(limits.MaxLogSegmentNumber)-body.FirstSegment {
		return invalidEngineBody("invalid log signing range")
	}
	tenant, subject, err := s.logSubmission(r, a, body.Subject)
	if err != nil {
		return engineLookupError(err)
	}
	class, err := tenant.PrepareLogWrite(r.Context(), subject, logActor(a, subject))
	if err != nil {
		return engineLookupError(err)
	}
	objects, err := s.submissionObjects()
	if err != nil {
		return err
	}
	batch, err := (logseg.BatchSigner{Objects: objects, ClassReady: objects.LogClassReady}).Sign(r.Context(), body, class)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, batch)
	return nil
}

type logCommitRequest struct {
	Subject          blob.LogSubject `json:"subject"`
	Segments         []store.Segment `json:"segments"`
	Final            bool            `json:"final"`
	IncompleteReason string          `json:"incomplete_reason,omitempty"`
}

type proposedLogSegments struct{ view store.LogSegments }

func (p proposedLogSegments) ReadLogSegments(context.Context, store.Actor, blob.LogReadRequest) (store.LogSegments, error) {
	return p.view, nil
}

func (s *Server) handleLogCommit(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body logCommitRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	tenant, subject, err := s.logSubmission(r, a, body.Subject)
	if err != nil {
		return engineLookupError(err)
	}
	actor := logActor(a, subject)
	class, err := tenant.PrepareLogWrite(r.Context(), subject, actor)
	if err != nil {
		return engineLookupError(err)
	}
	objects, err := s.submissionObjects()
	if err != nil {
		return err
	}
	for i := range body.Segments {
		key, err := blob.LogObjectKey(subject, class, body.Segments[i].Index)
		if err != nil {
			return invalidEngineBody("invalid segment index")
		}
		if body.Segments[i].ObjectKey != "" && body.Segments[i].ObjectKey != key {
			return invalidEngineBody("invalid segment object")
		}
		body.Segments[i].ObjectKey = key
	}
	reader := logseg.CommittedReader{Objects: objects, Records: proposedLogSegments{store.LogSegments{Subject: subject, Segments: body.Segments}}}
	offset := int64(0)
	if len(body.Segments) > 0 {
		offset = body.Segments[0].Offset
	}
	if _, err := reader.Read(r.Context(), actor, blob.LogReadRequest{Subject: subject, Offset: offset}, io.Discard); err != nil {
		return err
	}
	if err := tenant.RecordSegments(r.Context(), store.SegmentCommit{Subject: subject, Actor: actor, Segments: body.Segments, Final: body.Final, IncompleteReason: body.IncompleteReason, RetentionClass: class}); err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
	return nil
}

func (s *Server) handleLiveLogSubmission(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body struct {
		Subject blob.LogSubject `json:"subject"`
		Offset  int64           `json:"offset"`
		Data    []byte          `json:"data"`
	}
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Offset < 0 || int64(len(body.Data)) > limits.LogSegmentBytes {
		return invalidEngineBody("invalid live log range")
	}
	tenant, subject, err := s.logSubmission(r, a, body.Subject)
	if err != nil {
		return engineLookupError(err)
	}
	if _, err := tenant.PrepareLogWrite(r.Context(), subject, logActor(a, subject)); err != nil {
		return engineLookupError(err)
	}
	encoded, err := json.Marshal(subject)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(encoded)
	node := hex.EncodeToString(sum[:])
	if subject.Attempt != nil {
		node = subject.Attempt.NodeID
	}
	if s.liveLogs == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "log storage is not configured"}
	}
	attemptID := hex.EncodeToString(sum[:])
	if subject.Attempt != nil {
		attemptID = store.LiveLogAttempt(subject.Attempt.Attempt, subject.Attempt.Generation)
	}
	s.liveLogs.AppendAt(subject.RunID, node, attemptID, body.Offset, body.Data)
	writeJSON(w, http.StatusOK, map[string]any{"watched": s.liveLogs.Watched(subject.RunID, node)})
	return nil
}

var (
	_ logSubmissionObjects = (*blob.Filesystem)(nil)
	_ logSubmissionObjects = (*blob.S3)(nil)
)
