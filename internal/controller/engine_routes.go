package controller

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierrhttp"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// WithEngineController supplies the engine and authority transaction adapter before
// Handler is constructed. ServeWith refuses an engine without a runner policy.
func (s *Server) WithEngineController(engine *EngineController) *Server {
	s.engineController = engine
	if engine != nil && engine.ResolveTenant != nil {
		s.resolveTenant = engine.ResolveTenant
	}
	return s
}

func (s *Server) engineEndpoints() []controllerwire.Endpoint {
	engine := s.engineController
	return []controllerwire.Endpoint{
		{Method: "POST", Pattern: "/api/v1/repositories/resolve", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleResolveRepository},
		{
			Method: "POST", Pattern: "/api/v1/runs", Authentication: capability.AuthCapability,
			Decode: engine.intakeTarget, Handle: s.handleIntake(engine),
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/plan", Authentication: capability.AuthCapability,
			Decode: engine.runResultTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handlePlan,
		},
		{
			Method: "GET", Pattern: "/api/v1/runs/{run}", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleRunDetail,
		},
		{
			Method: "GET", Pattern: "/api/v1/runs/{run}/snapshot", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleSnapshot,
		},
		{
			Method: "GET", Pattern: "/api/v1/runs/{run}/jobs", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleJobs,
		},
		{
			Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleJob,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/finish", Authentication: capability.AuthCapability,
			Decode: engine.runResultTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleFinish,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/cancel", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleCancel,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/cancel", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleCancelJob,
		},
		{
			Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/failure", Authentication: capability.AuthCapability,
			Decode: engine.failureTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleFailure,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/retry", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleRetry(engine),
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/heartbeat", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleRenewal,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/transition", Authentication: capability.AuthCapability,
			Decode: engine.runResultTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleTransition,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/heartbeat", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleRenewal,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/expansion", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveExpansionTarget, Handle: engine.handleExpansion,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/annotations", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleAnnotation,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/steps/annotations", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleAnnotation,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/summary", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleSummary,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/steps/summary", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleSummary,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/steps/start", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleStep,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/steps/finish", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleStep,
		},
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/steps/skip", Authentication: capability.AuthCapability,
			Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: engine.handleStep,
		},
	}
}

func (s *EngineController) handleExpansion(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	body, err := decodeExpansion(r)
	if err != nil {
		return err
	}
	accepted, err := s.AcceptExpansion(r.Context(), a, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, accepted)
	return nil
}

func (s *EngineController) runResultTarget(r *http.Request) (capability.RequestEvidence, error) {
	evidence, _ := runSelectorTarget(r)
	data, err := readEngineBody(r)
	if err != nil {
		return evidence, err
	}
	digest := sha256.Sum256(data)
	evidence.ResultDigest = hex.EncodeToString(digest[:])
	if evidence.Target.NodeID != "" {
		var transition struct {
			SourceAttempt *int64 `json:"source_attempt"`
		}
		if err := json.Unmarshal(data, &transition); err != nil {
			return evidence, invalidEngineBody("invalid transition body")
		}
		evidence.ResultAttempt = transition.SourceAttempt
	}
	return evidence, nil
}

func (s *EngineController) intakeTarget(r *http.Request) (capability.RequestEvidence, error) {
	var body controllerwire.CreateRunRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return capability.RequestEvidence{}, err
	}
	target := capability.Target{
		Team: body.Request.Team, BodyRunID: body.Request.Lineage.ParentRunID,
		RunID: body.Request.Lineage.ParentRunID, NodeID: body.Request.Lineage.ParentJobID,
	}
	return capability.RequestEvidence{Target: target}, nil
}

func (s *Server) handleIntake(engine *EngineController) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		var body controllerwire.CreateRunRequest
		if err := decodeEngineRequest(r, &body); err != nil {
			return err
		}
		if err := validateGitSHA(body.Request.Git.SHA); err != nil {
			return invalidEngineBody(err.Error())
		}
		intent, err := IntakeForStore(a, body)
		if err != nil {
			return err
		}
		replay, found, err := engine.replayRun(r.Context(), a.Target.Team, intent)
		if err != nil {
			return err
		}
		if found {
			writeJSON(w, http.StatusOK, replay)
			return nil
		}
		if err := s.resolveIntakeSource(r.Context(), a.Target.Team, &body.Request); err != nil {
			return err
		}
		if err := s.resolveChildRevisions(r.Context(), engine, a, &body.Request); err != nil {
			return err
		}
		fetched, err := s.selectFetchedSource(r.Context(), &body.Request, s.appSourceSelector(store.Team(a.Target.Team)))
		if err != nil {
			return err
		}
		placementSource := fetched
		compared, credentialVerified, err := s.verifyIntakeSourceCredential(r.Context(), store.Team(a.Target.Team), body.Request)
		unverifiedReason := ""
		if err != nil {
			var unavailable *intakeCompareUnavailable
			if !errors.As(err, &unavailable) {
				return err
			}
			fetched = store.FetchedRepository{}
			unverifiedReason = "Run is unverified because " + unavailable.Error()
		}
		if fetched.ID == 0 && compared.ID > 0 {
			fetched = compared
		}
		if placementSource.ID == 0 {
			placementSource = compared
		}
		ctx := context.WithValue(r.Context(), verifiedIntakeSourceKey{}, verifiedIntakeSource{location: body.Request.Source.Location, repository: body.Request.Git.Repository})
		request, err := IntakeForStore(a, body)
		if err != nil {
			return err
		}
		request.Intent = &intent.Request
		request.FetchedRepository = fetched
		if request.RepositoryPlacement, err = s.requestPlacement(ctx, store.Team(a.Target.Team), body.Request, placementSource); err != nil {
			return err
		}
		request.SourceCredentialVerified = credentialVerified
		request.UnverifiedReason = unverifiedReason
		response, err := admitWithFlood(ctx, s, func(ctx context.Context) (controllerwire.CreateRunResponse, error) {
			return engine.admitRun(ctx, a.Target.Team, request)
		})
		if err != nil {
			if refusal := publicQueueDepthRefusal(err); refusal != nil {
				return apierrhttp.Write(w, refusal)
			}
			return err
		}
		status := http.StatusCreated
		if response.Replayed {
			status = http.StatusOK
		}
		writeJSON(w, status, response)
		return nil
	}
}

func (s *EngineController) handlePlan(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.PlanSubmission
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.AcceptPlan(r.Context(), a, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func readEngineBody(r *http.Request) ([]byte, error) {
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		return nil, invalidEngineBody("content-type application/json required")
	}
	body := r.Body
	defer body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(nil, body, maxPlanSnapshotBody))
	if err != nil {
		return nil, invalidEngineBody("invalid engine request body")
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

func decodeEngineRequest(r *http.Request, body any) error {
	data, err := readEngineBody(r)
	if err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return invalidEngineBody("request requires one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		refusal := invalidEngineBody(engineDecodeMessage(err))
		if field, ok := unknownEngineField(err); ok {
			refusal.Details = map[string]any{hosterr.DetailUnknownField: field}
		}
		return refusal
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return invalidEngineBody("request requires one JSON object")
	}
	return nil
}

// safety: decoder errors quote request bytes, so only offsets and fixed text reach the caller.
func engineDecodeMessage(err error) string {
	var syntax *json.SyntaxError
	var mistyped *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("invalid engine request: body is not valid JSON at byte %d; send one complete JSON object", syntax.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "invalid engine request: body ends before its JSON object is complete; send one complete JSON object"
	case errors.As(err, &mistyped):
		return fmt.Sprintf("invalid engine request: value at byte %d has the wrong JSON type for its field", mistyped.Offset)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		if field, ok := unknownEngineField(err); ok {
			return fmt.Sprintf("invalid engine request: this controller does not accept the field %q, which a newer client sends; upgrade the controller, or use a client no newer than the controller", field)
		}
		return "invalid engine request: body has a field this endpoint does not accept; upgrade the controller, or use a client no newer than the controller"
	}
	return "invalid engine request"
}

// safety: the field name comes from request bytes, so only a short identifier is echoed back.
var engineFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func unknownEngineField(err error) (string, bool) {
	quoted, found := strings.CutPrefix(err.Error(), "json: unknown field ")
	if !found {
		return "", false
	}
	field, unquoteErr := strconv.Unquote(quoted)
	if unquoteErr != nil || !engineFieldName.MatchString(field) {
		return "", false
	}
	return field, true
}

func engineLookupError(err error) error {
	if errors.Is(err, store.ErrCronScheduleTaken) {
		return &apierr.Error{Code: apierr.Conflict, Message: "another schedule holds this identity"}
	}
	var quota *store.StorageQuotaError
	if errors.As(err, &quota) {
		return &apierr.Error{Code: hosterr.QuotaExceeded, Message: quota.Error()}
	}
	if errors.Is(err, store.ErrStorageQuota) {
		return &apierr.Error{Code: hosterr.QuotaExceeded, Message: err.Error()}
	}
	var integrity *apierr.Error
	if errors.As(err, &integrity) && integrity.Code == hosterr.IntegrityFailure {
		return errors.Join(&apierr.Error{Code: hosterr.Internal, Message: "stored state is invalid"}, err)
	}
	var unpriced *store.UnpricedCPUClassError
	if errors.As(err, &unpriced) {
		return &apierr.Error{Code: apierr.Conflict, Message: unpriced.Error(), Details: map[string]any{"reason": unpricedCPUClassCode}}
	}
	var sqlErr interface{ SQLState() string }
	if errors.As(err, &sqlErr) {
		switch sqlErr.SQLState() {
		case "40001", "40P01", "55P03":
			return errors.Join(&apierr.Error{Code: hosterr.Unavailable, Message: "transaction is temporarily unavailable", Details: map[string]any{"retry_after_ms": 1000}}, err)
		}
	}
	var transition *store.ErrIllegalTransition
	if errors.As(err, &transition) {
		code := hosterr.IllegalTransition
		if transition.Reason == store.ReasonHolderRights || transition.Reason == store.ReasonStaleGeneration {
			code = hosterr.LockHeld
		}
		return &apierr.Error{Code: code, Message: "state transition refused", Details: map[string]any{"reason": string(transition.Reason)}}
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return &apierr.Error{Code: apierr.NotFound, Message: "resource not found"}
	}
	return err
}

func decodeExpansion(r *http.Request) (controllerwire.ExpansionSubmission, error) {
	var wire struct {
		SourceGeneration int64                      `json:"source_generation"`
		Body             json.RawMessage            `json:"body"`
		Registration     runspec.SecretRegistration `json:"registration"`
		Failure          json.RawMessage            `json:"failure"`
	}
	if err := decodeEngineRequest(r, &wire); err != nil {
		return controllerwire.ExpansionSubmission{}, err
	}
	body := controllerwire.ExpansionSubmission{SourceGeneration: wire.SourceGeneration, Body: wire.Body, Registration: wire.Registration}
	if len(wire.Failure) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Failure), []byte("null")) {
		failure, err := apierr.DecodeInner(wire.Failure)
		if err != nil {
			return controllerwire.ExpansionSubmission{}, invalidEngineBody("invalid expansion failure")
		}
		body.Failure = failure
	}
	return body, nil
}
