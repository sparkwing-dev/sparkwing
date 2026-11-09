// Package apierr defines the error body shared by HTTP, node protocol and JSON CLI output.
package apierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Code is the sole wire error discriminator. Clients branch on it rather than status or message.
type Code string

// Registered codes are additive within a protocol major.
const (
	BadRequest           Code = "bad_request"
	InvalidArgs          Code = "invalid_args"
	NotFound             Code = "not_found"
	Conflict             Code = "conflict"
	PlanInvalid          Code = "plan_invalid"
	PlanFailed           Code = "plan_failed"
	IsolatedHomeRequired Code = "isolated_home_required"
	ProtocolMismatch     Code = "protocol_mismatch"
	ProtocolError        Code = "protocol_error"
	UnknownOp            Code = "unknown_op"
	UnknownPipeline      Code = "unknown_pipeline"
	UnknownJob           Code = "unknown_job"
	UnknownStep          Code = "unknown_step"
	UnknownClosure       Code = "unknown_closure"
	NoPlan               Code = "no_plan"
	SDKError             Code = "sdk_error"
)

// Reason supplies stable forbidden details without reviving the old auth error envelopes.
type Reason string

const (
	ReasonNotReady Reason = "not_ready"
	ReasonNoOutput Reason = "no_output"
)

// Error emits Message, Hint and Details as given, so callers must supply safe, masked values.
// For secret_in_identifier, identify the identifier's field/path, never echo its value.
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// IsolatedHomeRefusal supplies recovery text without echoing SDK error payloads.
func IsolatedHomeRefusal() *Error {
	return &Error{Code: IsolatedHomeRequired, Message: `pipeline requires an isolated Sparkwing home; retry with HOME="$(mktemp -d)" SPARKWING_HOME="$(mktemp -d)" before the sparkwing command`}
}

// Error lets transport-neutral errors pass through normal Go error paths.
func (e *Error) Error() string { return e.Message }

// DecodeInner validates the one inner error object used by every transport.
// It rejects null, duplicate keys, trailing JSON, missing or wrongly typed code/message,
// invalid hint/details and an outer {error:...} envelope. Additive fields and unknown
// nonempty codes remain readable; the host owns the registered code vocabulary.
// Protocol replies, body failures, eval failures and recovery inputs all use this
// decoder before accepting an Error. HTTP/CLI readers unwrap their transport envelope.
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
