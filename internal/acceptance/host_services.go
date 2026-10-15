package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type processServiceRoutes struct {
	endpoints map[string]controller.Endpoint
}

func (r *processServiceRoutes) Register(endpoint controller.Endpoint) error {
	r.endpoints[endpoint.Pattern] = endpoint
	return nil
}

// CheckHostServices exercises actual endpoint bodies with policy and persistence
// fakes. It does not establish HTTP authentication or either SDK's service client.
func CheckHostServices(t *testing.T) {
	t.Helper()
	masker := secrets.NewMasker()
	registry := nodehost.NewMaskRegistry(masker)
	job := nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "step"}}}
	if err := registry.Accept(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	service := &nodehost.ProcessServices{RunID: "run", Job: job, Masks: registry}
	var annotation nodeproto.AnnotationRequest
	var summary nodeproto.SummaryRequest
	service.Annotation = func(_ context.Context, _ capability.Authorized, request nodeproto.AnnotationRequest) error {
		annotation = request
		return nil
	}
	service.Summary = func(_ context.Context, _ capability.Authorized, request nodeproto.SummaryRequest) error {
		summary = request
		return nil
	}
	auth := capability.Authorized{Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job"}}
	routes := &processServiceRoutes{endpoints: map[string]controller.Endpoint{}}
	if err := nodehost.RegisterProcessServices(routes, func(context.Context, capability.Authorized) (*nodehost.ProcessServices, error) { return service, nil }); err != nil {
		t.Fatal(err)
	}
	call := func(path string, body any) error {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
		endpoint, ok := routes.endpoints[path]
		if !ok || endpoint.Authentication != capability.AuthCapability {
			t.Fatal("missing authenticated service endpoint")
		}
		evidence, err := endpoint.Decode(request)
		if err != nil {
			return err
		}
		if evidence.ResultDigest == "" {
			t.Fatal("exact service body was not bound")
		}
		reply := httptest.NewRecorder()
		if err := endpoint.Handle(reply, request, auth); err != nil {
			return err
		}
		if strings.TrimSpace(reply.Body.String()) != "{}" {
			t.Fatal("service write was not acknowledged")
		}
		return nil
	}
	if err := call("/node/v1/masks", nodeproto.MaskRequest{Value: "private-secret"}); err != nil {
		t.Fatal(err)
	}
	if masker.Mask("private-secret") != "***" || !registry.ContainsSecret("private-secret-job") {
		t.Fatal("acknowledged mask was absent from sinks or validation")
	}
	err := call("/node/v1/masks", nodeproto.MaskRequest{Value: "j"})
	var collision *apierr.Error
	if !errors.As(err, &collision) || collision.Code != apierr.SecretInIdentifier {
		t.Fatal("accepted a short mask colliding with a persisted identifier")
	}
	if err := call("/node/v1/annotations", nodeproto.AnnotationRequest{Level: "warn", Message: "private-secret", File: "private-secret.go", Line: 2, EndLine: 4, Column: 1, Step: "step"}); err != nil {
		t.Fatal(err)
	}
	if annotation.Message != "***" || annotation.File != "***.go" || annotation.EndLine != 4 || annotation.Step != "step" {
		t.Fatalf("annotation=%+v", annotation)
	}
	if err := call("/node/v1/summary", nodeproto.SummaryRequest{Markdown: "# private-secret", Step: "step"}); err != nil {
		t.Fatal(err)
	}
	if summary.Markdown != "# ***" || summary.Step != "step" {
		t.Fatalf("summary=%+v", summary)
	}
	if err := call("/node/v1/summary", nodeproto.SummaryRequest{Markdown: "unknown step", Step: "other"}); err == nil {
		t.Fatal("record attributed to an undeclared step")
	}
}
