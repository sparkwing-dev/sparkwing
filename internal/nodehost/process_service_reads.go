package nodehost

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ProcessOutput carries an authorized recorded output, including explicit null.
type ProcessOutput struct {
	RunID  string          `json:"run_id,omitempty"`
	Output json.RawMessage `json:"output"`
}

// ProcessOIDCToken carries a minted token that is masked before delivery.
type ProcessOIDCToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ProcessChildResult carries the authorized child's persisted status and optional output.
type ProcessChildResult struct {
	RunID  string          `json:"run_id"`
	Status string          `json:"status"`
	Error  *apierr.Error   `json:"error,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}

type processOIDCRequest struct {
	Audience string `json:"audience"`
}

type processCleanupRequest struct {
	Argv        []string `json:"argv"`
	Description string   `json:"description"`
}

func (s *ProcessServices) read(request *http.Request, auth capability.Authorized, path string) (any, int, error) {
	ctx := request.Context()
	var body any
	var err error
	status := http.StatusOK
	switch path {
	case "/node/v1/secrets/{name}":
		body, err = s.secret(request, auth)
	case "/node/v1/jobs/{job}/output":
		if s.Output == nil {
			return nil, 0, serviceUnavailable()
		}
		var raw json.RawMessage
		raw, err = s.Output(ctx, auth, request.PathValue("job"))
		if err == nil {
			err = serviceOutput(raw)
		}
		body = ProcessOutput{Output: raw}
	case "/node/v1/pipelines/{name}/latest":
		if s.Latest == nil {
			return nil, 0, serviceUnavailable()
		}
		var age time.Duration
		age, err = serviceDuration(request.URL.Query().Get("max_age_ms"))
		if err != nil || request.URL.Query().Get("job") == "" {
			return nil, 0, executorError(apierr.BadRequest, "latest output requires a job and valid max_age_ms")
		}
		var output ProcessOutput
		output, err = s.Latest(ctx, auth, request.PathValue("name"), request.URL.Query().Get("job"), age)
		if err == nil {
			err = serviceOutput(output.Output)
			if output.RunID == "" {
				err = executorError(apierr.ProtocolError, "latest output has no source run")
			}
		}
		body = output
	case "/node/v1/oidc-token":
		body, err = s.oidc(request, auth)
	case "/node/v1/children":
		if s.StartChild == nil {
			return nil, 0, serviceUnavailable()
		}
		var child ChildRequest
		if err := decodeService(request, &child); err != nil {
			return nil, 0, err
		}
		if child.Pipeline == "" {
			return nil, 0, executorError(apierr.BadRequest, "child request requires a pipeline")
		}
		args, marshalErr := json.Marshal(child.Args)
		if marshalErr != nil {
			return nil, 0, marshalErr
		}
		if s.Masks.ContainsSecret(string(args)) {
			return nil, 0, executorError(apierr.BadRequest, "child arguments cannot contain a secret")
		}
		var id string
		id, err = s.StartChild(ctx, auth, child)
		if err == nil && id == "" {
			err = executorError(apierr.ProtocolError, "child admission returned no run")
		}
		body, status = struct {
			RunID string `json:"run_id"`
		}{id}, http.StatusCreated
	case "/node/v1/children/{run}/await":
		body, err = s.awaitChild(request, auth)
	default:
		err = executorError(apierr.UnsupportedRoute, "unknown process service")
	}
	return body, status, err
}

func (s *ProcessServices) secret(request *http.Request, auth capability.Authorized) (any, error) {
	name := request.PathValue("name")
	if !executorContains(s.Secrets, name) {
		return nil, &apierr.Error{Code: apierr.Forbidden, Message: "secret is not declared", Details: map[string]any{"reason": apierr.ReasonSecretUndeclared}}
	}
	if s.Secret == nil {
		return nil, serviceUnavailable()
	}
	value, masked, err := s.Secret(request.Context(), auth, name)
	if err != nil {
		var canonical *apierr.Error
		if errors.As(err, &canonical) {
			return nil, &apierr.Error{Code: canonical.Code, Message: "secret could not be read"}
		}
		return nil, serviceUnavailable()
	}
	if masked {
		if err := s.Masks.registerIssued(value); err != nil {
			return nil, err
		}
	}
	return struct {
		Value  string `json:"value"`
		Masked bool   `json:"masked"`
	}{value, masked}, nil
}

func (s *ProcessServices) oidc(request *http.Request, auth capability.Authorized) (any, error) {
	if s.OIDC == nil {
		return nil, serviceUnavailable()
	}
	var body processOIDCRequest
	if err := decodeService(request, &body); err != nil {
		return nil, err
	}
	if body.Audience == "" {
		return nil, executorError(apierr.BadRequest, "OIDC requires an audience")
	}
	token, err := s.OIDC(request.Context(), auth, body.Audience)
	if err != nil {
		return nil, serviceUnavailable()
	}
	if token.Token == "" || token.ExpiresAt.IsZero() {
		return nil, executorError(apierr.ProtocolError, "OIDC returned no token or expiry")
	}
	if err := s.Masks.registerIssued(token.Token); err != nil {
		return nil, err
	}
	return token, nil
}

func (s *ProcessServices) awaitChild(request *http.Request, auth capability.Authorized) (any, error) {
	if s.AwaitChild == nil {
		return nil, serviceUnavailable()
	}
	if s.activity != nil {
		defer s.activity.wait()()
	}
	wait, ok := waitParam(request.URL.Query().Get("wait_ms"))
	if !ok {
		return nil, executorError(apierr.BadRequest, "invalid child wait_ms")
	}
	result, err := s.AwaitChild(request.Context(), auth, request.PathValue("run"), request.URL.Query().Get("job"), wait)
	if err != nil {
		return nil, err
	}
	if result.RunID != request.PathValue("run") || result.Status == "" {
		return nil, executorError(apierr.ProtocolError, "child response changed its run or omitted status")
	}
	if len(result.Output) != 0 {
		if err := serviceOutput(result.Output); err != nil {
			return nil, err
		}
	}
	if result.Error != nil {
		masked := maskHostError(result.Error, s.Masks.masker)
		if !errors.As(masked, &result.Error) {
			return nil, executorError(apierr.Internal, "child error could not be encoded")
		}
	}
	return result, nil
}

func serviceOutput(raw json.RawMessage) error {
	if !json.Valid(raw) {
		return executorError(apierr.ProtocolError, "recorded output is missing or invalid JSON")
	}
	return checkOutput(raw)
}

func serviceDuration(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
		return 0, executorError(apierr.BadRequest, "invalid duration in milliseconds")
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func serviceUnavailable() error {
	return executorError(apierr.Unavailable, "process service is unavailable")
}
