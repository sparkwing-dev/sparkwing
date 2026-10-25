package nodeproto

import "github.com/sparkwing-dev/sparkwing/internal/apierr"

// errorUse names a canonical error and its refusal detail at one protocol boundary.
type errorUse struct {
	Code    apierr.Code
	Surface string
	Reason  apierr.Reason
}

// errorUses inventories consumers; apierr.Codes remains the sole code/status registry.
func errorUses() []errorUse {
	return []errorUse{
		{apierr.UnknownOp, "sdk", ""},
		{apierr.UnknownPipeline, "sdk", ""},
		{apierr.UnknownJob, "sdk,node_route", ""},
		{apierr.UnknownStep, "sdk", ""},
		{apierr.UnknownClosure, "sdk", ""},
		{apierr.NoPlan, "sdk", ""},
		{apierr.BadRequest, "sdk,node_route", ""},
		{apierr.InvalidArgs, "sdk", ""},
		{apierr.PlanFailed, "sdk", ""},
		{apierr.PlanDrift, "host", ""},
		{apierr.PlanInvalid, "host,declaration_cycle", ""},
		{apierr.ProtocolMismatch, "hello", ""},
		{apierr.ProtocolError, "host", ""},
		{apierr.SDKExited, "host", ""},
		{apierr.FramesClosed, "host", ""},
		{apierr.SDKError, "sdk", ""},
		{apierr.Unauthenticated, "node_route", ""},
		{apierr.Forbidden, "node_route", apierr.ReasonMissingGrant},
		{apierr.LockHeld, "host,node_route", ""},
		{apierr.IllegalTransition, "host,node_route", ""},
		{apierr.SpecDrift, "host", ""},
		{apierr.Forbidden, "secret_undeclared", apierr.ReasonSecretUndeclared},
		{apierr.NotFound, "secret_not_found,no_run,unknown_child", ""},
		{apierr.Unavailable, "secret_unavailable,upstream_unavailable,oidc_unavailable", ""},
		{apierr.Conflict, "output_not_ready", apierr.ReasonNotReady},
		{apierr.NotFound, "no_output", apierr.ReasonNoOutput},
		{apierr.Forbidden, "not_dependency", apierr.ReasonNotDependency},
		{apierr.Forbidden, "reference_undeclared", apierr.ReasonReferenceUndeclared},
		{apierr.RateLimited, "node_route", ""},
		{apierr.BadRequest, "runtime_child_cycle", apierr.ReasonChildCycle},
		{apierr.Forbidden, "child_repo", apierr.ReasonRepositoryUnauthorized},
	}
}
