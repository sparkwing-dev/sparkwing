package nodehost

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/hosthelpers"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"

	helperruntime "github.com/sparkwing-dev/sparkwing/internal/hosthelpers/runtime"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// ProcessServices supplies one live process's accepted declaration and record writers.
// Writers recheck Authorized in their transaction. Every sink uses Masks' shared masker.
type ProcessServices struct {
	lifetime       context.Context
	activity       *nativeProcessActivity
	SourceBindings capability.BindingChecker
	RunID          string
	Job            nodewire.JobSpec
	Masks          *MaskRegistry
	Annotation     func(context.Context, capability.Authorized, nodewire.AnnotationRequest) error
	Summary        func(context.Context, capability.Authorized, nodewire.SummaryRequest) error
	Secrets        []string
	Secret         func(context.Context, capability.Authorized, string) (string, bool, error)
	Output         func(context.Context, capability.Authorized, string) (json.RawMessage, error)
	Latest         func(context.Context, capability.Authorized, string, string, time.Duration, bool) (ProcessOutput, error)
	OIDC           func(context.Context, capability.Authorized, string) (ProcessOIDCToken, error)
	StartChild     func(context.Context, capability.Authorized, ChildRequest) (string, error)
	AwaitChild     func(context.Context, capability.Authorized, string, string, time.Duration) (ProcessChildResult, error)
	Cleanups       *ProcessCleanups
	HelperStream   func(context.Context, string, nodewire.FrameLog, []byte, bool) error
}

var helperSequence atomic.Uint64

// ProcessEndpointRegistrar is implemented by the common policy router.
type ProcessEndpointRegistrar interface {
	Register(controllerwire.Endpoint) error
}

// RegisterProcessServices installs actual capability-authenticated service handlers.
// Resolve selects the process from server-owned authority after policy authorization.
func RegisterProcessServices(router ProcessEndpointRegistrar, resolve func(context.Context, capability.Authorized) (*ProcessServices, error)) error {
	if router == nil || resolve == nil {
		return errors.New("process services require policy router and live resolver")
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/node/v1/helpers"},
		{http.MethodPost, "/node/v1/masks"},
		{http.MethodPost, "/node/v1/annotations"},
		{http.MethodPost, "/node/v1/summary"},
		{http.MethodGet, "/node/v1/secrets/{name}"},
		{http.MethodGet, "/node/v1/jobs/{job}/output"},
		{http.MethodGet, "/node/v1/pipelines/{name}/latest"},
		{http.MethodPost, "/node/v1/oidc-token"},
		{http.MethodPost, "/node/v1/children"},
		{http.MethodGet, "/node/v1/children/{run}/await"},
		{http.MethodPost, "/node/v1/cleanups"},
		{http.MethodDelete, "/node/v1/cleanups/{cleanup}"},
	} {
		path := route.path
		endpoint := controllerwire.Endpoint{Method: route.method, Pattern: path, Authentication: capability.AuthCapability, Decode: func(request *http.Request) (capability.RequestEvidence, error) {
			return serviceEvidence(request, path)
		}, ResolveTarget: processServiceTarget}
		endpoint.Handle = func(w http.ResponseWriter, request *http.Request, auth capability.Authorized) error {
			service, err := resolve(request.Context(), auth)
			if err != nil {
				code := hosterr.Unavailable
				var failure *apierr.Error
				if errors.As(err, &failure) {
					for _, definition := range hosterr.Codes {
						if failure.Code == definition.Code {
							code = failure.Code
							break
						}
					}
				}
				return executorError(code, "resolve process service failed")
			}
			if service == nil || service.Masks == nil || service.Masks.masker == nil || service.RunID != auth.Binding.RunID || auth.Binding.Kind != capability.BindingTrigger && service.Job.ID != auth.Binding.NodeID || service.RunID == "" || service.Job.ID == "" {
				return executorError(hosterr.SpecDrift, "service binding differs from its live process")
			}
			if service.lifetime != nil {
				ctx, cancel := context.WithCancel(request.Context())
				stop := context.AfterFunc(service.lifetime, cancel)
				defer func() { stop(); cancel() }()
				if service.lifetime.Err() != nil {
					cancel()
				}
				request = request.WithContext(ctx)
			}
			if err := context.Cause(request.Context()); err != nil {
				return processServiceError(err, service.Masks.masker)
			}
			body, status, err := service.serve(request, auth, path)
			err = errors.Join(err, context.Cause(request.Context()))
			if err != nil {
				return processServiceError(err, service.Masks.masker)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			if status == http.StatusNoContent {
				return nil
			}
			return json.NewEncoder(w).Encode(body)
		}
		if err := router.Register(endpoint); err != nil {
			return err
		}
	}
	return nil
}

func processServiceError(err error, masker *secrets.Masker) error {
	var canonical *apierr.Error
	if !errors.As(err, &canonical) {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			err = errors.Join(executorError(hosterr.DeadlineExceeded, "process service deadline exceeded"), err)
		case errors.Is(err, context.Canceled):
			err = errors.Join(executorError(hosterr.Cancelled, "process service was cancelled"), err)
		}
	}
	return maskHostError(err, masker)
}

func processServiceTarget(_ context.Context, principal capability.Principal, target capability.Target) (capability.Target, error) {
	bound := principal.Capability.Binding
	if target.Team == "" {
		target.Team = principal.Capability.Team
	}
	if target.Reference != nil {
		return target, nil
	}
	if target.RunID == "" {
		target.RunID = bound.RunID
	}
	if target.NodeID == "" {
		target.NodeID = bound.NodeID
	}
	if target.BodyRunID == "" {
		target.BodyRunID = bound.RunID
	}
	return target, nil
}

func serviceEvidence(request *http.Request, pattern string) (capability.RequestEvidence, error) {
	evidence := capability.RequestEvidence{Target: capability.Target{NodeID: request.PathValue("job"), ChildRunID: request.PathValue("run"), CleanupID: request.PathValue("cleanup")}}
	switch pattern {
	case "/node/v1/secrets/{name}":
		evidence.Target.SecretName = request.PathValue("name")
	case "/node/v1/pipelines/{name}/latest":
		evidence.Target = capability.Target{Reference: &capability.PipelineReferenceTarget{Pipeline: request.PathValue("name"), JobID: request.URL.Query().Get("job"), FromAnyRun: request.URL.Query().Get("from_any_run") == "true", FromProvenRun: request.URL.Query().Get("from_proven_run") == "true"}}
	}
	if evidence.Target.Reference == nil && evidence.Target.NodeID == "" {
		evidence.Target.NodeID = request.URL.Query().Get("job")
	}
	if request.Method == http.MethodGet || request.Method == http.MethodDelete {
		return evidence, nil
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, limits.JSONRequestBytes+1))
	if err != nil {
		return capability.RequestEvidence{}, executorError(apierr.BadRequest, "cannot read service request")
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	if int64(len(body)) > limits.JSONRequestBytes {
		return capability.RequestEvidence{}, executorError(hosterr.PayloadTooLarge, "service request exceeds JSON envelope limit")
	}
	sum := sha256.Sum256(body)
	evidence.ResultDigest = hex.EncodeToString(sum[:])
	return evidence, nil
}

func (s *ProcessServices) serve(request *http.Request, auth capability.Authorized, path string) (any, int, error) {
	switch path {
	case "/node/v1/helpers":
		var body nodewire.HelperRequest
		if err := decodeService(request, &body); err != nil {
			return nil, 0, err
		}
		ctx := request.Context()
		if s.Cleanups != nil {
			ctx = helperruntime.WithCleanups(ctx, s.Cleanups.register, s.Cleanups.finish)
		}
		if body.Operation == "docker.Login" {
			var args []json.RawMessage
			if json.Unmarshal(body.Args, &args) == nil && len(args) == 3 {
				var secret string
				if json.Unmarshal(args[2], &secret) == nil {
					if err := s.Masks.registerValue(secret); err != nil {
						return nil, 0, err
					}
				}
			}
		}
		var finishOutput func() error
		if body.Operation == "docker.Run" {
			var args []struct {
				CaptureOutput bool `json:"captureOutput"`
			}
			if json.Unmarshal(body.Args, &args) == nil && len(args) == 1 && !args[0].CaptureOutput {
				if s.HelperStream == nil {
					return nil, 0, serviceUnavailable()
				}
				key := fmt.Sprintf("helper:%d:", helperSequence.Add(1))
				writer := func(stream string) helperOutputWriter {
					return func(chunk []byte) (int, error) {
						base := nodewire.FrameLog{TS: time.Now().UTC().Format(time.RFC3339Nano), Level: "info", Step: body.Step, Event: "exec_line"}
						err := s.HelperStream(ctx, key+stream, base, chunk, false)
						return len(chunk), err
					}
				}
				ctx = helperruntime.WithOutput(ctx, writer("stdout"), writer("stderr"))
				finishOutput = func() error {
					for _, stream := range []string{"stdout", "stderr"} {
						if err := s.HelperStream(ctx, key+stream, nodewire.FrameLog{Level: "info", Step: body.Step, Event: "exec_line"}, nil, true); err != nil {
							return err
						}
					}
					return nil
				}
			}
		}
		response := hosthelpers.Run(ctx, body)
		if finishOutput != nil {
			if err := finishOutput(); err != nil {
				return nil, 0, err
			}
		}
		response.Error = s.Masks.mask(response.Error)
		return response, http.StatusOK, nil
	case "/node/v1/masks", "/node/v1/annotations", "/node/v1/summary":
		return nodewire.ServiceAcknowledged{}, http.StatusOK, s.write(request, auth, path)
	case "/node/v1/cleanups":
		if s.Cleanups == nil {
			return nil, 0, serviceUnavailable()
		}
		var body processCleanupRequest
		if err := decodeService(request, &body); err != nil {
			return nil, 0, err
		}
		id, err := s.Cleanups.register(body.Argv, body.Description)
		return struct {
			ID string `json:"id"`
		}{id}, http.StatusCreated, err
	case "/node/v1/cleanups/{cleanup}":
		if s.Cleanups == nil {
			return nil, 0, serviceUnavailable()
		}
		return nil, http.StatusNoContent, s.Cleanups.release(request.PathValue("cleanup"))
	default:
		return s.read(request, auth, path)
	}
}

func (s *ProcessServices) write(request *http.Request, auth capability.Authorized, path string) error {
	ctx := request.Context()
	switch path {
	case "/node/v1/masks":
		var body nodewire.MaskRequest
		if err := decodeService(request, &body); err != nil {
			return err
		}
		return s.Masks.registerValue(body.Value)
	case "/node/v1/annotations":
		var body nodewire.AnnotationRequest
		if err := decodeService(request, &body); err != nil {
			return err
		}
		if body.Level == "" {
			body.Level = "info"
		}
		if err := nodeproto.ValidateAnnotation(body.Annotation); err != nil {
			return executorError(apierr.BadRequest, err.Error())
		}
		if err := s.step(body.Step); err != nil {
			return err
		}
		if s.Annotation == nil {
			return errors.New("annotation persistence is unavailable")
		}
		body.Message = limits.Record(s.Masks.mask(body.Message))
		body.File = s.Masks.mask(body.File)
		return s.Annotation(ctx, auth, body)
	case "/node/v1/summary":
		var body nodewire.SummaryRequest
		if err := decodeService(request, &body); err != nil {
			return err
		}
		if err := s.step(body.Step); err != nil {
			return err
		}
		if s.Summary == nil {
			return errors.New("summary persistence is unavailable")
		}
		body.Markdown = limits.Record(s.Masks.mask(body.Markdown))
		return s.Summary(ctx, auth, body)
	}
	return executorError(hosterr.UnsupportedRoute, "unknown process service")
}

func (s *ProcessServices) step(id string) error {
	if id != "" && !slices.ContainsFunc(s.Job.Steps, func(step nodewire.StepSpec) bool { return step.ID == id }) {
		return executorError(apierr.UnknownStep, "record step is not declared by the bound job")
	}
	return nil
}

func decodeService(request *http.Request, target any) error {
	bound := limits.JSONRequestBytes
	switch target.(type) {
	case *ChildRequest:
		bound = maxChildBody
	case *processCleanupRequest:
		bound = maxCleanupBody
	case *processOIDCRequest:
		bound = 4 << 10
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, bound+1))
	if err != nil || int64(len(raw)) > bound {
		return executorError(apierr.BadRequest, "cannot decode service request")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return executorError(apierr.BadRequest, "service request must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return executorError(apierr.BadRequest, "invalid service request")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return executorError(apierr.BadRequest, "duplicate service request field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return executorError(apierr.BadRequest, "invalid service request")
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return executorError(apierr.BadRequest, "service request fields cannot be null")
		}

	}
	required := []string{}
	switch target.(type) {
	case *nodewire.MaskRequest:
		required = []string{"value"}
	case *nodewire.AnnotationRequest:
		required = []string{"message"}
	case *nodewire.SummaryRequest:
		required = []string{"markdown"}
	}
	for _, key := range required {
		if !seen[key] {
			return executorError(apierr.BadRequest, "service request is missing a required field")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return executorError(apierr.BadRequest, "invalid service request")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return executorError(apierr.BadRequest, "service request has trailing input")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return executorError(apierr.BadRequest, "invalid service request fields")
	}
	return nil
}

type helperOutputWriter func([]byte) (int, error)

func (write helperOutputWriter) Write(chunk []byte) (int, error) { return write(chunk) }
