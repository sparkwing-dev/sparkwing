// Package apierr owns host error policy and HTTP error encoding.
package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
	"io"
	"math"
	"strconv"
	"time"
)

const (
	Unauthenticated       apierr.Code = "unauthenticated"
	Forbidden             apierr.Code = "forbidden"
	UnsupportedRoute      apierr.Code = "unsupported_route"
	MethodNotAllowed      apierr.Code = "method_not_allowed"
	LockHeld              apierr.Code = "lock_held"
	IllegalTransition     apierr.Code = "illegal_transition"
	SpecDrift             apierr.Code = "spec_drift"
	PlanDrift             apierr.Code = "plan_drift"
	PayloadTooLarge       apierr.Code = "payload_too_large"
	LimitExceeded         apierr.Code = "limit_exceeded"
	CapabilityUnsupported apierr.Code = "capability_unsupported"
	SecretInIdentifier    apierr.Code = "secret_in_identifier"
	InsufficientCredit    apierr.Code = "insufficient_credit"
	QuotaExceeded         apierr.Code = "quota_exceeded"
	RateLimited           apierr.Code = "rate_limited"
	Unavailable           apierr.Code = "unavailable"
	Internal              apierr.Code = "internal"
	PolicyMissing         apierr.Code = "policy_missing"
	IntegrityFailure      apierr.Code = "integrity_failure"
	BlobMissing           apierr.Code = "blob_missing"
	SDKExited             apierr.Code = "sdk_exited"
	FramesClosed          apierr.Code = "frames_closed"
	Cancelled             apierr.Code = "cancelled"
	DeadlineExceeded      apierr.Code = "deadline_exceeded"
	VersionRevoked        apierr.Code = "version_revoked"
	VersionUnsupported    apierr.Code = "version_unsupported"
)

const (
	ReasonMissingGrant           apierr.Reason = "missing_grant"
	ReasonAudience               apierr.Reason = "audience"
	ReasonBinding                apierr.Reason = "binding"
	ReasonTeam                   apierr.Reason = "team"
	ReasonNoTeam                 apierr.Reason = "no_team"
	ReasonCancelled              apierr.Reason = "cancel_requested"
	ReasonSecretUndeclared       apierr.Reason = "secret_undeclared"
	ReasonDigestNotReadable      apierr.Reason = "digest_not_readable"
	ReasonRepositoryUnauthorized apierr.Reason = "repository_unauthorized"
	ReasonReadOnly               apierr.Reason = "read_only"
	ReasonPersonalMachine        apierr.Reason = "personal_machine"
	ReasonCredentialUnconfirmed  apierr.Reason = "credential_unconfirmed"
	ReasonReauthRequired         apierr.Reason = "reauth_required"
	ReasonSecretsKeyRequired     apierr.Reason = "secrets_key_required"
	ReasonExchangeUsed           apierr.Reason = "exchange_used"
)

// Definition drives error reference documentation and HTTP status selection.
type Definition struct {
	Code       apierr.Code
	HTTPStatus int
	Purpose    string
}

// Codes is the complete target registry. Job outcome reasons are not API error codes.
var Codes = []Definition{
	{apierr.BadRequest, 400, "Malformed or invalid request."},
	{apierr.InvalidArgs, 400, "Pipeline arguments do not validate."},
	{Unauthenticated, 401, "No live credential for the route liveness class."},
	{Forbidden, 403, "A live credential lacks authority; details.reason identifies the refusal."},
	{apierr.NotFound, 404, "The authorized resource does not exist."},
	{UnsupportedRoute, 404, "The server does not register this route."},
	{MethodNotAllowed, 405, "The path exists but does not accept this method."},
	{LockHeld, 409, "A transaction lost its holder or generation fence."},
	{IllegalTransition, 409, "The state edge or replay payload is not legal."},
	{apierr.Conflict, 409, "An existing resource conflicts with the requested change."},
	{SpecDrift, 409, "A sealed spec differs from the submitted spec."},
	{PlanDrift, 409, "Reconstructed code differs from the persisted job specification."},
	{PayloadTooLarge, 413, "The request exceeds the named size limit."},
	{apierr.PlanInvalid, 422, "A plan or expansion violates its contract."},
	{apierr.PlanFailed, 422, "Pipeline planning failed."},
	{apierr.IsolatedHomeRequired, 422, "Pipeline requires a temporary home; the host supplies fixed recovery text before secret discovery completes."},
	{LimitExceeded, 422, "An explicit setting exceeds its ceiling."},
	{CapabilityUnsupported, 422, "The placement capability table refuses a declaration."},
	{SecretInIdentifier, 422, "A stored identifier contains a registered secret value."},
	{InsufficientCredit, 402, "The team cannot reserve the requested compute."},
	{QuotaExceeded, 429, "An atomic shared quota reservation failed."},
	{RateLimited, 429, "The request may retry after the stated delay."},
	{Unavailable, 503, "A required service is temporarily unavailable."},
	{Internal, 500, "An internal failure occurred; private diagnostics stay server-side."},
	{PolicyMissing, 500, "A registered route has no policy row."},
	{IntegrityFailure, 422, "Transferred bytes fail size or digest verification."},
	{BlobMissing, 404, "An authorized blob is absent or tombstoned."},
	{apierr.ProtocolMismatch, 400, "Hello cannot agree on a protocol major."},
	{apierr.ProtocolError, 400, "A protocol frame violates the negotiated contract."},
	{apierr.UnknownOp, 400, "The protocol operation is not supported."},
	{apierr.UnknownPipeline, 404, "The SDK did not declare the requested pipeline."},
	{apierr.UnknownJob, 404, "The SDK did not declare the requested job."},
	{apierr.UnknownStep, 404, "The SDK did not declare the requested step."},
	{apierr.UnknownClosure, 404, "The SDK did not declare the requested closure."},
	{apierr.NoPlan, 409, "The process has not accepted a plan for this operation."},
	{SDKExited, 502, "The SDK process exited before replying."},
	{FramesClosed, 502, "The protocol stream closed before replying."},
	{apierr.SDKError, 422, "An SDK or user function reported an unclassified failure."},
	{Cancelled, 409, "The operation was cancelled."},
	{DeadlineExceeded, 408, "The applicable execution, queue or approval deadline passed."},
	{VersionRevoked, 422, "Signed policy revoked the pinned engine; details give version and reason, hint names sparkwing pipeline upgrade."},
	{VersionUnsupported, 422, "An effective signed minimum refuses the engine or runner; details give the minimum, reason and update action."},
}

// Authorization constructs a forbidden error with its required reason.
func Authorization(reason apierr.Reason, message string) *apierr.Error {
	return &apierr.Error{Code: Forbidden, Message: message, Details: map[string]any{"reason": reason}}
}

// Envelope is the one outer error object. Protocol replies embed its Error beside reply and ok.
type Envelope struct {
	Error *apierr.Error `json:"error"`
}

// Status returns the registry status; an unregistered code is an internal failure.
func Status(code apierr.Code) int {
	for _, definition := range Codes {
		if definition.Code == code {
			return definition.HTTPStatus
		}
	}
	return 500
}

// WriteJSON emits one newline-terminated Envelope for CLI stderr and other JSON streams.
func WriteJSON(w io.Writer, failure *apierr.Error) error {
	encoded, err := encode(failure)
	if err != nil {
		return err
	}
	n, err := w.Write(encoded)
	if err == nil && n != len(encoded) {
		return io.ErrShortWrite
	}
	return err
}

func encode(failure *apierr.Error) ([]byte, error) {
	if failure == nil {
		return nil, errors.New("missing error")
	}
	registered := false
	for _, definition := range Codes {
		if definition.Code == failure.Code {
			registered = true
			break
		}
	}
	if !registered {
		return nil, errors.New("unregistered error code")
	}
	encoded, err := json.Marshal(Envelope{Error: failure})
	if err != nil {
		return nil, fmt.Errorf("encode error: %w", err)
	}
	return append(encoded, '\n'), nil
}

// RetryAfter returns the Retry-After seconds for failure's details.retry_after_ms, or "" when it names none.
func RetryAfter(failure *apierr.Error) string {
	if failure == nil {
		return ""
	}
	return retryAfter(failure.Details["retry_after_ms"])
}

func retryAfter(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	millis, err := strconv.ParseFloat(string(encoded), 64)
	if err != nil || millis < 0 || math.IsNaN(millis) || math.IsInf(millis, 0) {
		return ""
	}
	return strconv.FormatFloat(math.Ceil(millis/1000), 'f', 0, 64)
}

// RetryDelay returns the whole-second wait a rate-limited or unavailable failure names in details.retry_after_ms.
func RetryDelay(err error) (time.Duration, bool) {
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != RateLimited && failure.Code != Unavailable {
		return 0, false
	}
	seconds, parseErr := strconv.ParseInt(retryAfter(failure.Details["retry_after_ms"]), 10, 64)
	if parseErr != nil {
		return 0, false
	}
	return time.Duration(min(seconds, int64(time.Hour/time.Second))) * time.Second, true
}

// DetailUnknownField names, in Error.Details, a request field a controller refused because it does not know it.
const DetailUnknownField = "unknown_field"
