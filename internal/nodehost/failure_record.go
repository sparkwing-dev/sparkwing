package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func recordNodeFailure(job string, result runstate.NodeResult, mask func(string) string) runstate.NodeResult {
	result.MaskedError = limits.Error(mask(result.MaskedError))
	result.Reason = limits.Error(mask(result.Reason))
	if result.Outcome != runstate.NodeFailed {
		result.Failure = nil
		return result
	}
	stage, code := "action", apierr.SDKError
	if failure := result.Failure; failure != nil {
		if failure.Stage == "verify" {
			stage = "verify"
		}
		if failure.Error != nil {
			code = maskedFailureCode(failure.Error.Code, mask)
		}
	} else if result.Reason != "" {
		code = maskedFailureCode(apierr.Code(result.Reason), mask)
	}
	result.Failure = &nodeproto.JobFailure{Job: job, Stage: stage, Error: &apierr.Error{Code: code, Message: result.MaskedError}}
	return result
}

func maskedFailureCode(code apierr.Code, mask func(string) string) apierr.Code {
	if code == "" || mask(string(code)) != string(code) {
		return apierr.SDKError
	}
	return code
}
