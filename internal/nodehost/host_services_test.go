package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type processServiceRoutes struct {
	endpoints map[string]controller.Endpoint
}

func (r *processServiceRoutes) Register(endpoint controller.Endpoint) error {
	r.endpoints[endpoint.Pattern] = endpoint
	return nil
}

func TestHostServices(t *testing.T) {
	t.Helper()
	masker := secrets.NewMasker()
	registry := NewMaskRegistry(masker)
	job := nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "step"}}}
	if err := registry.acceptPlan(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	service := &ProcessServices{RunID: "run", Job: job, Masks: registry}
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
	if err := RegisterProcessServices(routes, func(context.Context, capability.Authorized) (*ProcessServices, error) { return service, nil }); err != nil {
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
	if !errors.Is(err, secrets.ErrInvalidValue) {
		t.Fatalf("short mask error = %v", err)
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
	checkRegisteredServiceReads(t, routes, service, auth)
}

func checkRegisteredServiceReads(t *testing.T, routes *processServiceRoutes, service *ProcessServices, auth capability.Authorized) {
	t.Helper()
	if len(routes.endpoints) != 11 {
		t.Fatal("node-host registration is incomplete")
	}
	service.Secrets = []string{"TOKEN"}
	service.Secret = func(context.Context, capability.Authorized, string) (string, bool, error) {
		return "released-secret", true, nil
	}
	service.Output = func(context.Context, capability.Authorized, string) (json.RawMessage, error) {
		return json.RawMessage(`null`), nil
	}
	service.Latest = func(context.Context, capability.Authorized, string, string, time.Duration) (ProcessOutput, error) {
		return ProcessOutput{RunID: "prior", Output: json.RawMessage(`{"value":7}`)}, nil
	}
	service.OIDC = func(context.Context, capability.Authorized, string) (ProcessOIDCToken, error) {
		return ProcessOIDCToken{Token: "minted-credential", ExpiresAt: time.Unix(2e9, 0)}, nil
	}
	service.StartChild = func(context.Context, capability.Authorized, ChildRequest) (string, error) {
		return "child", nil
	}
	service.AwaitChild = func(context.Context, capability.Authorized, string, string, time.Duration) (ProcessChildResult, error) {
		return ProcessChildResult{RunID: "child", Status: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "released-secret"}}, nil
	}
	service.Cleanups = &ProcessCleanups{}
	for _, test := range []struct {
		method, path, target, body, want string
		status                           int
	}{
		{"GET", "/node/v1/secrets/{name}", "/node/v1/secrets/TOKEN", "", `"value":"released-secret"`, 200},
		{"GET", "/node/v1/jobs/{job}/output", "/node/v1/jobs/producer/output", "", `"output":null`, 200},
		{"GET", "/node/v1/pipelines/{name}/latest", "/node/v1/pipelines/build/latest?job=producer", "", `"run_id":"prior"`, 200},
		{"POST", "/node/v1/oidc-token", "/node/v1/oidc-token", `{"audience":"cloud"}`, `"token":"minted-credential"`, 200},
		{"POST", "/node/v1/children", "/node/v1/children", `{"pipeline":"build"}`, `"run_id":"child"`, 201},
		{"GET", "/node/v1/children/{run}/await", "/node/v1/children/child/await", "", `"message":"***"`, 200},
		{"POST", "/node/v1/cleanups", "/node/v1/cleanups", `{"argv":["tool"]}`, `"id":"1"`, 201},
		{"DELETE", "/node/v1/cleanups/{cleanup}", "/node/v1/cleanups/1", "", "", 204},
	} {
		endpoint, ok := routes.endpoints[test.path]
		if !ok || endpoint.Method != test.method {
			t.Fatalf("missing service %s %s", test.method, test.path)
		}
		request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
		for key, value := range map[string]string{"name": "TOKEN", "job": "producer", "run": "child", "cleanup": "1"} {
			request.SetPathValue(key, value)
		}
		evidence, err := endpoint.Decode(request)
		if err != nil {
			t.Fatal(err)
		}
		auth.Target = evidence.Target
		reply := httptest.NewRecorder()
		if err := endpoint.Handle(reply, request, auth); err != nil || reply.Code != test.status || !strings.Contains(reply.Body.String(), test.want) {
			t.Fatalf("%s %s: status=%d body=%s error=%v", test.method, test.path, reply.Code, reply.Body, err)
		}
	}
	if !service.Masks.ContainsSecret("released-secret") || !service.Masks.ContainsSecret("minted-credential") {
		t.Fatal("service released a credential before masking it")
	}
}
