package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// ProcessServices supplies one live process's accepted declaration and record writers.
// Writers recheck Authorized in their transaction. Every sink uses Masks' shared masker.
type ProcessServices struct {
	RunID      string
	Job        nodeproto.JobSpec
	Masks      *MaskRegistry
	Annotation func(context.Context, capability.Authorized, nodeproto.AnnotationRequest) error
	Summary    func(context.Context, capability.Authorized, nodeproto.SummaryRequest) error
}

// ProcessEndpointRegistrar is implemented by the common policy router.
type ProcessEndpointRegistrar interface {
	Register(controller.Endpoint) error
}

// RegisterProcessServices installs actual capability-authenticated service handlers.
// Resolve selects the process from server-owned authority after policy authorization.
func RegisterProcessServices(router ProcessEndpointRegistrar, resolve func(context.Context, capability.Authorized) (*ProcessServices, error)) error {
	if router == nil || resolve == nil {
		return errors.New("process services require policy router and live resolver")
	}
	for _, path := range []string{"/node/v1/masks", "/node/v1/annotations", "/node/v1/summary"} {
		endpoint := controller.Endpoint{Method: http.MethodPost, Pattern: path, Authentication: capability.AuthCapability, Decode: serviceEvidence}
		endpoint.Handle = func(w http.ResponseWriter, request *http.Request, auth capability.Authorized) error {
			service, err := resolve(request.Context(), auth)
			if err != nil {
				return maskHostError(err, func(string) string { return "resolve process service failed" })
			}
			if service == nil || service.Masks == nil || service.Masks.masker == nil || service.RunID != auth.Binding.RunID || service.Job.ID != auth.Binding.NodeID || service.RunID == "" || service.Job.ID == "" {
				return executorError(apierr.SpecDrift, "service binding differs from its live process")
			}
			if err := service.write(request, auth, path); err != nil {
				return maskHostError(err, service.Masks.mask)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			return json.NewEncoder(w).Encode(nodeproto.ServiceAcknowledged{})
		}
		if err := router.Register(endpoint); err != nil {
			return err
		}
	}
	return nil
}

func serviceEvidence(request *http.Request) (capability.RequestEvidence, error) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, limits.JSONRequestBytes+1))
	if err != nil {
		return capability.RequestEvidence{}, executorError(apierr.BadRequest, "cannot read service request")
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	if int64(len(body)) > limits.JSONRequestBytes {
		return capability.RequestEvidence{}, executorError(apierr.PayloadTooLarge, "service request exceeds JSON envelope limit")
	}
	sum := sha256.Sum256(body)
	return capability.RequestEvidence{ResultDigest: hex.EncodeToString(sum[:])}, nil
}

func (s *ProcessServices) write(request *http.Request, auth capability.Authorized, path string) error {
	ctx := request.Context()
	switch path {
	case "/node/v1/masks":
		var body nodeproto.MaskRequest
		if err := decodeService(request, &body); err != nil {
			return err
		}
		return s.Masks.Register(body.Value)
	case "/node/v1/annotations":
		var body nodeproto.AnnotationRequest
		if err := decodeService(request, &body); err != nil {
			return err
		}
		if !logLevels[body.Level] || body.Line < 0 || body.EndLine < 0 || body.Column < 0 {
			return executorError(apierr.BadRequest, "invalid annotation level or location")
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
		var body nodeproto.SummaryRequest
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
	return executorError(apierr.UnsupportedRoute, "unknown process service")
}

func (s *ProcessServices) step(id string) error {
	if id != "" && !slices.ContainsFunc(s.Job.Steps, func(step nodeproto.StepSpec) bool { return step.ID == id }) {
		return executorError(apierr.UnknownStep, "record step is not declared by the bound job")
	}
	return nil
}

func decodeService(request *http.Request, target any) error {
	raw, err := io.ReadAll(io.LimitReader(request.Body, limits.JSONRequestBytes+1))
	if err != nil || int64(len(raw)) > limits.JSONRequestBytes {
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
	case *nodeproto.MaskRequest:
		required = []string{"value"}
	case *nodeproto.AnnotationRequest:
		required = []string{"level", "message"}
	case *nodeproto.SummaryRequest:
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
