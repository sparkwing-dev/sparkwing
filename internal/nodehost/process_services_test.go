package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestMaskRegistryRefusesAcceptedIdentifiersBeforeRegistering(t *testing.T) {
	for _, value := range []string{"b", "build", "body", "group", "new\nbuild", "YnVpbGQ="} {
		t.Run(value, func(t *testing.T) {
			m := secrets.NewMasker()
			registry := NewMaskRegistry(m)
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Groups: []string{"group"}, Steps: []nodeproto.StepSpec{{ID: "body"}, {ID: "encoded-YnVpbGQ="}}}}}
			if err := registry.Accept(plan, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			err := registry.Register(value)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.SecretInIdentifier || !strings.Contains(failure.Message, "jobs[0]") || len(m.Values()) != 0 {
				t.Fatalf("collision=%v, registry=%v", err, m.Values())
			}
		})
	}
}

func TestMaskRegistryProtectsLaterExpansionAndFailedAcceptance(t *testing.T) {
	masker := secrets.NewMasker()
	registry := NewMaskRegistry(masker)
	if err := registry.Register("secret"); err != nil || masker.Mask("secret") != "***" || !registry.ContainsSecret("prefix-secret") {
		t.Fatal("registration was not visible to all sinks and validators")
	}
	committed := false
	if err := registry.Accept(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "secret-job"}}}, func() error { committed = true; return nil }); err == nil || committed {
		t.Fatal("persisted a secret-bearing generated job")
	}
	sentinel := errors.New("store refused")
	if err := registry.Accept(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "unused"}}}, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := registry.Register("unused"); err != nil {
		t.Fatal("refused transaction left phantom identifiers")
	}
}

type serviceRegistrar struct{ endpoints []controller.Endpoint }

func (r *serviceRegistrar) Register(endpoint controller.Endpoint) error {
	r.endpoints = append(r.endpoints, endpoint)
	return nil
}

func TestProcessServicesPreserveResolutionRefusalCodes(t *testing.T) {
	for _, definition := range apierr.Codes {
		t.Run(string(definition.Code), func(t *testing.T) {
			routes := &serviceRegistrar{}
			failure := &apierr.Error{Code: definition.Code, Message: "private-credential", Hint: "private-credential", Details: map[string]any{"private-credential": "private-credential"}}
			if err := RegisterProcessServices(routes, func(context.Context, capability.Authorized) (*ProcessServices, error) { return nil, failure }); err != nil {
				t.Fatal(err)
			}
			_, err := callService(t, routes, capability.Authorized{}, "/node/v1/masks", `{"value":"mask-value"}`)
			var rendered *apierr.Error
			if !errors.As(err, &rendered) || rendered.Code != definition.Code || strings.Contains(rendered.Error(), "private-credential") || rendered.Hint != "" || rendered.Details != nil {
				t.Fatalf("resolution refusal changed code or exposed diagnostics: %+v", rendered)
			}
		})
	}
}

func serviceFixture(t *testing.T) (*serviceRegistrar, *ProcessServices, capability.Authorized) {
	t.Helper()
	masker := secrets.NewMasker()
	service := &ProcessServices{RunID: "run", Job: nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "step"}}}, Masks: NewMaskRegistry(masker)}
	authorized := capability.Authorized{Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job"}}
	routes := &serviceRegistrar{}
	if err := RegisterProcessServices(routes, func(context.Context, capability.Authorized) (*ProcessServices, error) { return service, nil }); err != nil {
		t.Fatal(err)
	}
	return routes, service, authorized
}

func callService(t *testing.T, routes *serviceRegistrar, authorized capability.Authorized, path, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	for _, endpoint := range routes.endpoints {
		if endpoint.Pattern != path {
			continue
		}
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		evidence, err := endpoint.Decode(request)
		if err != nil {
			return httptest.NewRecorder(), err
		}
		if evidence.ResultDigest == "" || endpoint.Authentication != capability.AuthCapability {
			t.Fatal("missing policy proof")
		}
		out := httptest.NewRecorder()
		return out, endpoint.Handle(out, request, authorized)
	}
	t.Fatal("missing service registration")
	return nil, nil
}

func TestProcessServicesAcknowledgeMaskAndPreserveBoundRecords(t *testing.T) {
	routes, service, auth := serviceFixture(t)
	if len(routes.endpoints) != 11 {
		t.Fatal("missing node service route")
	}
	out, err := callService(t, routes, auth, "/node/v1/masks", `{"value":"credential"}`)
	if err != nil || strings.TrimSpace(out.Body.String()) != "{}" {
		t.Fatalf("mask acknowledgement=%s, %v", out.Body.String(), err)
	}
	var annotation nodeproto.AnnotationRequest
	service.Annotation = func(_ context.Context, _ capability.Authorized, request nodeproto.AnnotationRequest) error {
		annotation = request
		return nil
	}
	out, err = callService(t, routes, auth, "/node/v1/annotations", `{"level":"warn","message":"credential","file":"credential.go","line":2,"end_line":4,"column":3,"step":"step"}`)
	if err != nil || annotation.Message != "***" || annotation.File != "***.go" || annotation.EndLine != 4 || annotation.Column != 3 || annotation.Step != "step" || strings.TrimSpace(out.Body.String()) != "{}" {
		t.Fatalf("annotation=%+v error=%v", annotation, err)
	}
	for _, body := range []string{`{"message":"default level"}`, `{"level":"","message":"default level"}`} {
		if _, err := callService(t, routes, auth, "/node/v1/annotations", body); err != nil || annotation.Level != "info" {
			t.Fatalf("default annotation level=%s error=%v", annotation.Level, err)
		}
	}
	var summary nodeproto.SummaryRequest
	service.Summary = func(_ context.Context, _ capability.Authorized, request nodeproto.SummaryRequest) error {
		summary = request
		return nil
	}
	body, _ := json.Marshal(nodeproto.SummaryRequest{Markdown: "credential" + strings.Repeat("界", int(limits.AnnotationBytes)) + "tail", Step: "step"})
	_, err = callService(t, routes, auth, "/node/v1/summary", string(body))
	if err != nil || len(summary.Markdown) > int(limits.AnnotationBytes) || !utf8.ValidString(summary.Markdown) || strings.Contains(summary.Markdown, "credential") || !strings.HasSuffix(summary.Markdown, "tail") {
		t.Fatalf("summary length=%d, error=%v", len(summary.Markdown), err)
	}
}

func TestProcessServicesRefuseUnknownStepsAndPropagateMaskedWrites(t *testing.T) {
	routes, service, auth := serviceFixture(t)
	if err := service.Masks.Register("credential"); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("credential write failed")
	calls := 0
	service.Annotation = func(context.Context, capability.Authorized, nodeproto.AnnotationRequest) error {
		calls++
		return sentinel
	}
	for _, body := range []string{`null`, `{"value":null}`, `{"level":"warning","message":"text"}`, `{"level":"warn","message":"text","step":"unknown"}`, `{"level":"warn","step":"step","step":"other"}`, `{"level":"warn"} {}`} {
		if _, err := callService(t, routes, auth, "/node/v1/annotations", body); err == nil {
			t.Fatal("accepted malformed record")
		}
	}
	if calls != 0 {
		t.Fatal("invalid record reached persistence")
	}
	_, err := callService(t, routes, auth, "/node/v1/annotations", `{"level":"warn","message":"valid"}`)
	if !errors.Is(err, sentinel) || strings.Contains(err.Error(), "credential") || calls != 1 {
		t.Fatalf("write error=%v, calls=%d", err, calls)
	}
	auth.Binding.NodeID = "other"
	if _, err := callService(t, routes, auth, "/node/v1/masks", `{"value":"new"}`); err == nil {
		t.Fatal("changed process identity accepted")
	}
}

func TestMaskRegistryJoinsRegistrationWithAcceptance(t *testing.T) {
	registry := NewMaskRegistry(secrets.NewMasker())
	entered, release := make(chan struct{}), make(chan struct{})
	accepted := make(chan error, 1)
	go func() {
		accepted <- registry.Accept(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}}, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	registered := make(chan error, 1)
	go func() { registered <- registry.Register("job") }()
	select {
	case err := <-registered:
		close(release)
		<-accepted
		t.Fatalf("registration passed an in-flight acceptance: %v", err)
	default:
	}
	close(release)
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if err := <-registered; err == nil {
		t.Fatal("registration acknowledged a secret-bearing persisted identifier")
	}
}

func TestOwnerHostSharesIdentifierRegistryWithAcceptance(t *testing.T) {
	f, host, input := newOwnerFixture(t, false)
	registry := NewMaskRegistry(secrets.NewMasker())
	host.Masks = registry
	job := nodeproto.JobSpec{ID: "persisted", Steps: []nodeproto.StepSpec{{ID: "body"}}}
	prepare := host.Prepare
	host.Prepare = func(ctx context.Context, in RunInput, entry *source.Entry) (OwnerPlanning, error) {
		planning, err := prepare(ctx, in, entry)
		planning.Plan = func(context.Context) (json.RawMessage, error) {
			return json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
		}
		return planning, err
	}
	if _, err := host.Run(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if f.accepted.RunID != "run" {
		t.Fatal("plan acceptance did not commit")
	}
	if err := registry.Register("persisted"); err == nil {
		t.Fatal("accepted job was missing from process mask registry")
	}
}
