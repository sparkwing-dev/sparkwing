package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type annotationRequest struct {
	StepID  string `json:"step_id,omitempty"`
	Level   string `json:"level,omitempty"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    *int   `json:"line,omitempty"`
}

type stepStartRequest struct {
	StepID string `json:"step_id"`
}

type stepFinishRequest struct {
	StepID string        `json:"step_id"`
	Status string        `json:"status"`
	Error  *apierr.Error `json:"error,omitempty"`
}

func (s *EngineController) handleSummary(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body struct {
		StepID   string `json:"step_id,omitempty"`
		Markdown string `json:"markdown"`
	}
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if err := reportingStep(r, body.StepID); err != nil {
		return err
	}
	if err := reportingText(body.Markdown); err != nil {
		return err
	}
	tenant, err := s.runTransactions(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := tenant.WriteSummary(r.Context(), store.SummaryWrite{
		Ref: store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}, Actor: engineActor(a), StepID: body.StepID, Markdown: body.Markdown,
	}); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *EngineController) handleStep(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	report, err := decodeStepReport(r)
	if err != nil {
		return err
	}
	tenant, err := s.runTransactions(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	report.Attempt, err = reportingAttempt(r.Context(), tenant, a)
	if err != nil {
		return err
	}
	report.Actor = engineActor(a)
	if err := tenant.RecordStep(r.Context(), report); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func decodeStepReport(r *http.Request) (store.StepReport, error) {
	report := store.StepReport{State: store.StepRunning}
	if strings.HasSuffix(r.Pattern, "/finish") {
		var body stepFinishRequest
		if err := decodeEngineRequest(r, &body); err != nil {
			return report, err
		}
		if err := store.ValidateStepTerminalStatus(body.Status); err != nil {
			return report, invalidEngineBody("step status must be passed, failed, or cancelled")
		}
		masked, err := engineMaskedError(body.Error)
		if err != nil {
			return report, err
		}
		report.StepID, report.State, report.MaskedError = body.StepID, body.Status, masked
	} else {
		var body stepStartRequest
		if err := decodeEngineRequest(r, &body); err != nil {
			return report, err
		}
		report.StepID = body.StepID
		if strings.HasSuffix(r.Pattern, "/skip") {
			report.State = store.StepSkipped
		}
	}
	return report, reportingStep(r, report.StepID)
}

func reportingAttempt(ctx context.Context, tenant RunTransactions, a capability.Authorized) (runspec.AttemptIdentity, error) {
	attempt := runspec.AttemptIdentity{Team: a.Target.Team, RunID: a.Target.RunID, NodeID: a.Target.NodeID,
		Attempt: a.Binding.Attempt, Generation: a.Binding.Generation}
	if a.Principal.Capability.Unit != capability.UnitNode {
		// safety: owners and publishers have no job-attempt fence in their own binding.
		node, err := tenant.ReadNode(ctx, store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID})
		if err != nil {
			return attempt, engineLookupError(err)
		}
		if node.Ref.RunID != a.Target.RunID || node.Ref.NodeID != a.Target.NodeID {
			return attempt, errors.New("inconsistent persisted reporting job")
		}
		attempt.Attempt, attempt.Generation = node.Attempt, node.Generation
	}
	if attempt.Attempt <= 0 || attempt.Generation <= 0 {
		return attempt, &apierr.Error{Code: apierr.IllegalTransition, Message: "job has no reporting attempt"}
	}
	return attempt, nil
}

func (s *EngineController) handleAnnotation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body annotationRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if err := reportingStep(r, body.StepID); err != nil {
		return err
	}
	if err := reportingText(body.Message); err != nil {
		return err
	}
	tenant, err := s.runTransactions(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := tenant.WriteAnnotation(r.Context(), store.AnnotationWrite{
		Ref: store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}, Actor: engineActor(a),
		StepID: body.StepID, Level: body.Level, Message: body.Message, File: body.File, Line: body.Line,
	}); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func reportingStep(r *http.Request, step string) error {
	if strings.Contains(r.Pattern, "/steps/") != (step != "") {
		return invalidEngineBody("step_id is required only on step routes")
	}
	return nil
}

func reportingText(text string) error {
	if int64(len(text)) > limits.AnnotationBytes {
		return &apierr.Error{Code: apierr.PayloadTooLarge, Message: "annotation or summary exceeds its byte limit", Details: map[string]any{"limit_bytes": limits.AnnotationBytes}}
	}
	return nil
}
