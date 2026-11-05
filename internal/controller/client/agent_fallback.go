package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// hack: controllers through v0.68.6 return this exact refusal for any request field they do not know.
const legacyUnknownField = "invalid engine request: body has a field this endpoint does not accept"

// UnknownRequestField reports whether err is a controller refusing a request field it does not know. The
// strict decoder refuses before intake writes anything, so the caller may resend without the field.
// Controllers that name the field return it; older controllers return an empty name.
func UnknownRequestField(err error) (string, bool) {
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.BadRequest {
		return "", false
	}
	if field, ok := refused.Details[apierr.DetailUnknownField].(string); ok && field != "" {
		return field, true
	}
	return "", refused.Message == legacyUnknownField
}

// CreateRun submits input and, when a controller older than the agent label refuses that field,
// resends once without it and writes a one-line notice to notice. The label is display-only, so
// dropping it changes no authority; every other refusal is returned unchanged.
func CreateRun(ctx context.Context, controller controllerwire.Controller, input controllerwire.CreateRunRequest, notice io.Writer) (controllerwire.CreateRunResponse, error) {
	response, err := controller.CreateRun(ctx, input)
	field, unknown := UnknownRequestField(err)
	agent := input.Request.Agent
	if !unknown || agent == "" || field != "" && field != "agent" {
		return response, err
	}
	input.Request.Agent = ""
	response, err = controller.CreateRun(ctx, input)
	if err == nil && notice != nil {
		_, _ = fmt.Fprintf(notice, "notice: the controller is older than this CLI and does not record agent labels; the run was submitted without agent %s\n", agent)
	}
	return response, err
}

// Retry submits a retry and, when a controller older than the retry reason refuses the reason or agent
// field, resends once without both and writes a one-line notice to notice. Such a controller records
// no reason and requires none, so dropping them changes no authority; every other refusal is returned unchanged.
func Retry(ctx context.Context, controller controllerwire.Controller, run string, input controllerwire.RetryRequest, notice io.Writer) (controllerwire.CreateRunResponse, error) {
	response, err := controller.Retry(ctx, run, input)
	field, unknown := UnknownRequestField(err)
	if !unknown || input.Reason == "" && input.Agent == "" || field != "" && field != "reason" && field != "agent" {
		return response, err
	}
	reason := input.Reason
	input.Reason, input.Agent = "", ""
	response, err = controller.Retry(ctx, run, input)
	if err == nil && notice != nil && reason != "" {
		_, _ = fmt.Fprintf(notice, "notice: the controller is older than this CLI and does not record retry reasons; run %s was retried without reason %q\n", run, reason)
	}
	return response, err
}
