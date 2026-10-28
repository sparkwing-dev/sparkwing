package client

import (
	"context"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var _ controllerwire.RunReview = (*engineClient)(nil)

// NewRunReview uses the same authenticated transport and route policies as engines.
func NewRunReview(options EngineOptions) (controllerwire.RunReview, error) {
	return newEngineClient(options)
}

func (c *engineClient) ListApprovals(ctx context.Context, run string) (controllerwire.Approvals, error) {
	var result controllerwire.Approvals
	pattern, path := "/api/v1/approvals", "/api/v1/approvals?pending=true"
	if run != "" {
		if !validSelector(run) {
			return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid run selector"}
		}
		pattern, path = "/api/v1/runs/{run}/approvals", "/api/v1/runs/"+url.PathEscape(run)+"/approvals"
	}
	err := c.request(ctx, "GET", pattern, path, "", nil, &result)
	if err == nil {
		for _, approval := range result.Approvals {
			if approval.RunID == "" || approval.JobID == "" || approval.RequestedAt.IsZero() || approval.ResolvedAt == nil && approval.Resolution != "" || approval.ResolvedAt != nil && approval.Resolution == "" {
				return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an incomplete approval"}
			}
		}
	}
	return result, err
}

func (c *engineClient) ResolveApproval(ctx context.Context, run, job string, input controllerwire.ApprovalDecision) (controllerwire.ApprovalResult, error) {
	var result controllerwire.ApprovalResult
	if !validSelector(run) || !validSelector(job) || input.Resolution != "approved" && input.Resolution != "denied" || input.IdempotencyKey == "" || strings.ContainsAny(input.IdempotencyKey, "\r\n\x00") {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "approval decision requires run, job, approved|denied resolution and an idempotency key"}
	}
	path := "/api/v1/runs/" + url.PathEscape(run) + "/approvals/" + url.PathEscape(job)
	err := c.request(ctx, "POST", "/api/v1/runs/{run}/approvals/{job}", path, input.IdempotencyKey, input, &result)
	if err == nil && (result.Approval.RunID == "" || result.Approval.JobID != job || result.Approval.RequestedAt.IsZero() || result.Approval.ResolvedAt == nil || result.Approval.Resolution != input.Resolution || result.Approval.Approver == "") {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an incomplete or conflicting approval decision"}
	}
	return result, err
}

func (c *engineClient) AppendAnnotation(ctx context.Context, run, job string, input controllerwire.AnnotationWrite) error {
	if !validSelector(run) || !validSelector(job) || input.StepID != "" && !validSelector(input.StepID) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "annotation requires valid run, job and step selectors"}
	}
	if input.Level == "" {
		input.Level = "info"
	}
	if err := input.Validate(); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	suffix := "/annotations"
	if input.StepID != "" {
		suffix = "/steps/annotations"
	}
	return c.request(ctx, "POST", "/api/v1/runs/{run}/jobs/{job}"+suffix, "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+suffix, "", input, nil)
}
