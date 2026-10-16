// Package apierr defines the error body shared by HTTP, node protocol and JSON CLI output.
package apierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
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

// Authorization constructs a forbidden error with its required reason.
func Authorization(reason Reason, message string) *Error {
	return &Error{Code: Forbidden, Message: message, Details: map[string]any{"reason": reason}}
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, errors.New("invalid error JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing error JSON")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("error must be an object")
	}
	if _, nested := object["error"]; nested {
		return nil, errors.New("expected inner error, received envelope")
	}
	code, codeOK := object["code"].(string)
	message, messageOK := object["message"].(string)
	if !codeOK || code == "" || !messageOK {
		return nil, errors.New("error requires string code and message")
	}
	failure := &Error{Code: Code(code), Message: message}
	if hint, exists := object["hint"]; exists {
		if failure.Hint, ok = hint.(string); !ok {
			return nil, errors.New("error hint must be a string")
		}
	}
	if details, exists := object["details"]; exists {
		if failure.Details, ok = details.(map[string]any); !ok {
			return nil, errors.New("error details must be an object")
		}
	}
	return failure, nil
}

// UnmarshalJSON validates inner errors with the same codec used by protocol replies.
func (e *Error) UnmarshalJSON(data []byte) error {
	failure, err := DecodeInner(data)
	if err != nil {
		return err
	}
	*e = *failure
	return nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	if delim == '{' {
		object := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid object key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate object key")
			}
			object[name], err = decodeValue(decoder)
			if err != nil {
				return nil, err
			}
		}
		_, err = decoder.Token()
		return object, err
	}
	if delim == '[' {
		var values []any
		for decoder.More() {
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err = decoder.Token()
		return values, err
	}
	return nil, errors.New("unexpected JSON delimiter")
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
	encoded, err := encode(failure)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	if delay := retryAfter(failure.Details["retry_after_ms"]); delay != "" {
		w.Header().Set("Retry-After", delay)
	}
	w.WriteHeader(Status(failure.Code))
	n, err := w.Write(encoded)
	if err == nil && n != len(encoded) {
		return io.ErrShortWrite
	}
	return err
}

// WriteJSON emits one newline-terminated Envelope for CLI stderr and other JSON streams.
func WriteJSON(w io.Writer, failure *Error) error {
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

func encode(failure *Error) ([]byte, error) {
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
