// Package apierr defines the error body shared by HTTP, node protocol and JSON CLI output.
package apierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Code is the sole wire error discriminator. Clients branch on it rather than status or message.
type Code string

// Registered codes are additive within a protocol major.
const (
	BadRequest            Code = "bad_request"
	InvalidArgs           Code = "invalid_args"
	Unauthenticated       Code = "unauthenticated"
	Forbidden             Code = "forbidden"
	NotFound              Code = "not_found"
	UnsupportedRoute      Code = "unsupported_route"
	MethodNotAllowed      Code = "method_not_allowed"
	LockHeld              Code = "lock_held"
	IllegalTransition     Code = "illegal_transition"
	Conflict              Code = "conflict"
	SpecDrift             Code = "spec_drift"
	PlanDrift             Code = "plan_drift"
	PayloadTooLarge       Code = "payload_too_large"
	PlanInvalid           Code = "plan_invalid"
	PlanFailed            Code = "plan_failed"
	LimitExceeded         Code = "limit_exceeded"
	CapabilityUnsupported Code = "capability_unsupported"
	SecretInIdentifier    Code = "secret_in_identifier"
	InsufficientCredit    Code = "insufficient_credit"
	QuotaExceeded         Code = "quota_exceeded"
	RateLimited           Code = "rate_limited"
	Unavailable           Code = "unavailable"
	Internal              Code = "internal"
	PolicyMissing         Code = "policy_missing"
	IntegrityFailure      Code = "integrity_failure"
	BlobMissing           Code = "blob_missing"
	ProtocolMismatch      Code = "protocol_mismatch"
	ProtocolError         Code = "protocol_error"
	UnknownOp             Code = "unknown_op"
	UnknownPipeline       Code = "unknown_pipeline"
	UnknownJob            Code = "unknown_job"
	UnknownStep           Code = "unknown_step"
	UnknownClosure        Code = "unknown_closure"
	NoPlan                Code = "no_plan"
	SDKExited             Code = "sdk_exited"
	FramesClosed          Code = "frames_closed"
	SDKError              Code = "sdk_error"
	Cancelled             Code = "cancelled"
	DeadlineExceeded      Code = "deadline_exceeded"
	VersionRevoked        Code = "version_revoked"
	VersionUnsupported    Code = "version_unsupported"
)

// Definition drives error reference documentation and HTTP status selection.
type Definition struct {
	Code       Code
	HTTPStatus int
	Purpose    string
}

// Codes is the complete target registry. Job outcome reasons are not API error codes.
var Codes = []Definition{
	{BadRequest, 400, "Malformed or invalid request."},
	{InvalidArgs, 400, "Pipeline arguments do not validate."},
	{Unauthenticated, 401, "No live credential for the route liveness class."},
	{Forbidden, 403, "A live credential lacks authority; details.reason identifies the refusal."},
	{NotFound, 404, "The authorized resource does not exist."},
	{UnsupportedRoute, 404, "The server does not register this route."},
	{MethodNotAllowed, 405, "The path exists but does not accept this method."},
	{LockHeld, 409, "A transaction lost its holder or generation fence."},
	{IllegalTransition, 409, "The state edge or replay payload is not legal."},
	{Conflict, 409, "An existing resource conflicts with the requested change."},
	{SpecDrift, 409, "A sealed spec differs from the submitted spec."},
	{PlanDrift, 409, "Reconstructed code differs from the persisted job specification."},
	{PayloadTooLarge, 413, "The request exceeds the named size limit."},
	{PlanInvalid, 422, "A plan or expansion violates its contract."},
	{PlanFailed, 422, "Pipeline planning failed."},
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
	{ProtocolMismatch, 400, "Hello cannot agree on a protocol major."},
	{ProtocolError, 400, "A protocol frame violates the negotiated contract."},
	{UnknownOp, 400, "The protocol operation is not supported."},
	{UnknownPipeline, 404, "The SDK did not declare the requested pipeline."},
	{UnknownJob, 404, "The SDK did not declare the requested job."},
	{UnknownStep, 404, "The SDK did not declare the requested step."},
	{UnknownClosure, 404, "The SDK did not declare the requested closure."},
	{NoPlan, 409, "The process has not accepted a plan for this operation."},
	{SDKExited, 502, "The SDK process exited before replying."},
	{FramesClosed, 502, "The protocol stream closed before replying."},
	{SDKError, 422, "An SDK or user function reported an unclassified failure."},
	{Cancelled, 409, "The operation was cancelled."},
	{DeadlineExceeded, 408, "The applicable execution, queue or approval deadline passed."},
	{VersionRevoked, 422, "Signed policy revoked the pinned engine; details give version and reason, hint names sparkwing upgrade."},
	{VersionUnsupported, 422, "An effective signed minimum refuses the engine or runner; details give the minimum, reason and update action."},
}

// Reason supplies stable forbidden details without reviving the old auth error envelopes.
type Reason string

const (
	ReasonMissingGrant           Reason = "missing_grant"
	ReasonAudience               Reason = "audience"
	ReasonBinding                Reason = "binding"
	ReasonTeam                   Reason = "team"
	ReasonNoTeam                 Reason = "no_team"
	ReasonNotReserved            Reason = "not_reserved"
	ReasonCancelled              Reason = "cancel_requested"
	ReasonSecretUndeclared       Reason = "secret_undeclared"
	ReasonInputUndeclared        Reason = "input_undeclared"
	ReasonSlotUndeclared         Reason = "slot_undeclared"
	ReasonDigestNotReadable      Reason = "digest_not_readable"
	ReasonRepositoryUnauthorized Reason = "repository_unauthorized"
	ReasonReadOnly               Reason = "read_only"
	ReasonOwnerOptInRequired     Reason = "owner_opt_in_required"
	ReasonCredentialUnconfirmed  Reason = "credential_unconfirmed"
	ReasonConsoleRequired        Reason = "console_required"
	ReasonReauthRequired         Reason = "reauth_required"
	ReasonSecretsKeyRequired     Reason = "secrets_key_required"
	ReasonNotDependency          Reason = "not_dependency"
	ReasonReferenceUndeclared    Reason = "reference_undeclared"
	ReasonNotReady               Reason = "not_ready"
	ReasonNoOutput               Reason = "no_output"
	ReasonChildCycle             Reason = "child_cycle"
)

// Error contains only safe, masked text. Details must never include registered secret values.
// For secret_in_identifier, identify the identifier's field/path, never echo its value.
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Error lets transport-neutral errors pass through normal Go error paths.
func (e *Error) Error() string { return e.Message }

// Envelope is the one outer error object. Protocol replies embed its Error beside reply and ok.
type Envelope struct {
	Error *Error `json:"error"`
}

// DecodeInner validates the one inner error object used by every transport.
// It rejects null, duplicate keys, trailing JSON, missing or wrongly typed code/message,
// invalid hint/details and an outer {error:...} envelope. Additive fields and unknown
// nonempty codes remain readable by older engines; producers use Codes membership.
// Protocol replies, body failures, eval failures and recovery inputs all use this
// decoder before accepting an Error. HTTP/CLI readers unwrap Envelope exactly once.
func DecodeInner(data []byte) (*Error, error) {
	invalid := errors.New("invalid inner error object")
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, invalid
	}
	if !uniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))) {
		return nil, invalid
	}
	if _, exists := fields["error"]; exists {
		return nil, invalid
	}
	code, ok := fields["code"].(string)
	if !ok || code == "" {
		return nil, invalid
	}
	message, ok := fields["message"].(string)
	if !ok {
		return nil, invalid
	}
	failure := &Error{Code: Code(code), Message: message}
	if hint, exists := fields["hint"]; exists {
		failure.Hint, ok = hint.(string)
		if !ok {
			return nil, invalid
		}
	}
	if details, exists := fields["details"]; exists {
		failure.Details, ok = details.(map[string]any)
		if !ok {
			return nil, invalid
		}
	}
	return failure, nil
}

func uniqueJSONKeys(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return false
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !uniqueJSONKeys(decoder) {
				return false
			}
		}
	case json.Delim('['):
		for decoder.More() {
			if !uniqueJSONKeys(decoder) {
				return false
			}
		}
	default:
		return true
	}
	_, err = decoder.Token()
	return err == nil
}

// Status returns the registry status; an unregistered code is an internal failure.
func Status(code Code) int {
	for _, definition := range Codes {
		if definition.Code == code {
			return definition.HTTPStatus
		}
	}
	return http.StatusInternalServerError
}

// WriteHTTP writes Envelope and derives status from Code, including retry_after_ms as Retry-After.
// Encode failures are returned to the caller for logging; this writer never emits plain text.
func WriteHTTP(w http.ResponseWriter, failure *Error) error {
	panic("unimplemented: c2-credentials")
}

// WriteJSON emits one newline-terminated Envelope for CLI stderr and other JSON streams.
func WriteJSON(w io.Writer, failure *Error) error {
	panic("unimplemented: c2-credentials")
}
