package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func registeredServiceRequest(t *testing.T, routes *serviceRegistrar, auth capability.Authorized, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, endpoint := range routes.endpoints {
		mux.HandleFunc(endpoint.Method+" "+endpoint.Pattern, func(w http.ResponseWriter, request *http.Request) {
			evidence, err := endpoint.Decode(request)
			if err == nil {
				auth.Target = evidence.Target
				err = endpoint.Handle(w, request, auth)
			}
			if err != nil {
				var failure *apierr.Error
				if !errors.As(err, &failure) {
					t.Fatalf("unstructured service refusal: %v", err)
				}
				w.WriteHeader(apierr.Status(failure.Code))
				if err := json.NewEncoder(w).Encode(apierr.Envelope{Error: failure}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(method, target, strings.NewReader(body)))
	return response
}

func TestProcessServicesRegisterCompleteNegotiatedSurface(t *testing.T) {
	routes, _, _ := serviceFixture(t)
	want := []string{
		"POST /node/v1/masks", "POST /node/v1/annotations", "POST /node/v1/summary",
		"GET /node/v1/secrets/{name}", "GET /node/v1/jobs/{job}/output", "GET /node/v1/pipelines/{name}/latest",
		"POST /node/v1/oidc-token", "POST /node/v1/children", "GET /node/v1/children/{run}/await",
		"POST /node/v1/cleanups", "DELETE /node/v1/cleanups/{cleanup}",
	}
	var got []string
	for _, route := range routes.endpoints {
		got = append(got, route.Method+" "+route.Pattern)
		if route.Authentication != capability.AuthCapability || route.Decode == nil {
			t.Fatal("service registration has no policy proof")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("service routes=%v", got)
	}
}

func FuzzProcessServiceNamedSelectorsBelongOnlyToTheirRoute(f *testing.F) {
	f.Add("TOKEN")
	f.Add("pipeline/with?syntax")
	f.Add("")
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 1024 {
			t.Skip()
		}
		routes, _, auth := serviceFixture(t)
		auth.Principal.Capability.Binding = auth.Binding
		auth.Principal.Capability.Team = "team"
		for _, endpoint := range routes.endpoints {
			if endpoint.Method != http.MethodGet {
				continue
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.SetPathValue("name", name)
			evidence, err := endpoint.Decode(request)
			if err != nil {
				t.Fatal(err)
			}
			wantSecret, wantPipeline := "", ""
			switch endpoint.Pattern {
			case "/node/v1/secrets/{name}":
				wantSecret = name
			case "/node/v1/pipelines/{name}/latest":
				wantPipeline = name
			}
			if evidence.Target.SecretName != wantSecret || (evidence.Target.Reference != nil && evidence.Target.Reference.Pipeline != wantPipeline || evidence.Target.Reference == nil && wantPipeline != "") {
				t.Fatalf("%s confused its secret and pipeline selectors", endpoint.Pattern)
			}
			if evidence.Target.Reference != nil {
				target, err := endpoint.ResolveTarget(t.Context(), auth.Principal, evidence.Target)
				if err != nil || target.RunID != "" || target.NodeID != "" || target.BodyRunID != "" || target.Reference == nil || target.Reference.Pipeline != name {
					t.Fatalf("reference acquired caller selectors: %+v: %v", target, err)
				}
			}
		}
	})
}

func TestProcessServicesReadUnderBoundAuthorityAndMaskBeforeDelivery(t *testing.T) {
	routes, service, auth := serviceFixture(t)
	service.Secrets = []string{"TOKEN", "REGION"}
	check := func(got capability.Authorized) {
		t.Helper()
		if got.Binding != auth.Binding {
			t.Fatal("service changed bound authority")
		}
	}
	service.Secret = func(_ context.Context, got capability.Authorized, name string) (string, bool, error) {
		check(got)
		if got.Target.SecretName != name {
			t.Fatal("secret selector was absent from authorization evidence")
		}
		return "private-secret", true, nil
	}
	response := registeredServiceRequest(t, routes, auth, http.MethodGet, "/node/v1/secrets/TOKEN", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"value":"private-secret"`) || service.Masks.mask("private-secret") != "***" {
		t.Fatalf("secret response=%d %s", response.Code, response.Body)
	}
	service.Output = func(_ context.Context, got capability.Authorized, id string) (json.RawMessage, error) {
		check(got)
		if id != "producer" || got.Target.NodeID != id {
			t.Fatal("output lost its authorized selector")
		}
		return json.RawMessage(`null`), nil
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodGet, "/node/v1/jobs/producer/output", "")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"output":null}` {
		t.Fatalf("output response=%d %s", response.Code, response.Body)
	}
	service.Latest = func(_ context.Context, got capability.Authorized, pipeline, id string, age time.Duration) (ProcessOutput, error) {
		check(got)
		if pipeline != "release" || id != "producer" || (got.Target.Reference == nil || got.Target.Reference.JobID != id || got.Target.Reference.Pipeline != pipeline) || age != 3*time.Second {
			t.Fatal("latest output selectors changed")
		}
		return ProcessOutput{RunID: "previous", Output: json.RawMessage(`{"image":"recorded"}`)}, nil
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodGet, "/node/v1/pipelines/release/latest?job=producer&max_age_ms=3000", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"run_id":"previous"`) {
		t.Fatalf("latest response=%d %s", response.Code, response.Body)
	}
	service.OIDC = func(_ context.Context, got capability.Authorized, audience string) (ProcessOIDCToken, error) {
		check(got)
		if audience != "cloud" {
			t.Fatal("OIDC audience changed")
		}
		return ProcessOIDCToken{Token: "minted-credential", ExpiresAt: time.Unix(2e9, 0)}, nil
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodPost, "/node/v1/oidc-token", `{"audience":"cloud"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"token":"minted-credential"`) || service.Masks.mask("minted-credential") != "***" {
		t.Fatalf("OIDC response=%d %s", response.Code, response.Body)
	}
	service.StartChild = func(_ context.Context, got capability.Authorized, child ChildRequest) (string, error) {
		check(got)
		if child.Pipeline != "deploy" || child.Args["target"] != "production" {
			t.Fatal("child arguments changed")
		}
		return "child", nil
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodPost, "/node/v1/children", `{"pipeline":"deploy","args":{"target":"production"}}`)
	if response.Code != http.StatusCreated || strings.TrimSpace(response.Body.String()) != `{"run_id":"child"}` {
		t.Fatalf("child response=%d %s", response.Code, response.Body)
	}
	service.AwaitChild = func(_ context.Context, got capability.Authorized, child, id string, wait time.Duration) (ProcessChildResult, error) {
		check(got)
		if child != "child" || got.Target.ChildRunID != child || id != "image" || got.Target.NodeID != id || wait != 5*time.Second {
			t.Fatal("child wait selectors changed")
		}
		return ProcessChildResult{RunID: child, Status: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "private-secret", Hint: "private-secret", Details: map[string]any{"private-secret": "private-secret"}}}, nil
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodGet, "/node/v1/children/child/await?job=image&wait_ms=5000", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "private-secret") || !strings.Contains(response.Body.String(), `"message":"***"`) {
		t.Fatalf("child wait response=%d %s", response.Code, response.Body)
	}
}

func TestProcessServicesRejectMissingOutputAndMalformedRequests(t *testing.T) {
	routes, service, auth := serviceFixture(t)
	calls := 0
	service.Output = func(context.Context, capability.Authorized, string) (json.RawMessage, error) { return nil, nil }
	response := registeredServiceRequest(t, routes, auth, http.MethodGet, "/node/v1/jobs/producer/output", "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"protocol_error"`) {
		t.Fatalf("missing output became success: %d %s", response.Code, response.Body)
	}
	service.StartChild = func(context.Context, capability.Authorized, ChildRequest) (string, error) {
		calls++
		return "child", nil
	}
	for _, body := range []string{`null`, `{}`, `{"pipeline":null}`, `{"pipeline":"first","pipeline":"second"}`, `{"pipeline":"deploy","args":null}`, `{"pipeline":"deploy","unknown":true}`, `{"pipeline":"deploy"} {}`} {
		response := registeredServiceRequest(t, routes, auth, http.MethodPost, "/node/v1/children", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed child accepted: %s -> %d %s", body, response.Code, response.Body)
		}
	}
	if calls != 0 {
		t.Fatal("malformed child reached admission")
	}
	for _, raw := range []string{"-1", "60001", "9223372036854775807", "9223372036854"} {
		if _, ok := waitParam(raw); ok {
			t.Fatalf("invalid child wait accepted: %s", raw)
		}
	}
}

func TestProcessServicesCleanupRegistrationIsProcessScoped(t *testing.T) {
	routes, service, auth := serviceFixture(t)
	service.Cleanups = &ProcessCleanups{}
	response := registeredServiceRequest(t, routes, auth, http.MethodPost, "/node/v1/cleanups", `{"argv":["tool","argument"],"description":"release"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("cleanup response=%d %s", response.Code, response.Body)
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodDelete, "/node/v1/cleanups/1", "")
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("cleanup release=%d %s", response.Code, response.Body)
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodDelete, "/node/v1/cleanups/1", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing cleanup=%d %s", response.Code, response.Body)
	}
	if err := service.Cleanups.run(t.Context(), CleanupScope{}); err != nil {
		t.Fatal(err)
	}
	response = registeredServiceRequest(t, routes, auth, http.MethodPost, "/node/v1/cleanups", `{"argv":["tool"]}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("registration after cleanup=%d %s", response.Code, response.Body)
	}
}

func FuzzProcessServiceBinding(f *testing.F) {
	f.Add("run", "job")
	f.Add("other", "job")
	f.Add("run", "other")
	f.Fuzz(func(t *testing.T, runID, jobID string) {
		routes, service, auth := serviceFixture(t)
		service.Secrets = []string{"TOKEN"}
		calls := 0
		service.Secret = func(context.Context, capability.Authorized, string) (string, bool, error) {
			calls++
			return "secret", true, nil
		}
		auth.Binding.RunID, auth.Binding.NodeID = runID, jobID
		response := registeredServiceRequest(t, routes, auth, http.MethodGet, "/node/v1/secrets/TOKEN", "")
		if runID != service.RunID || jobID != service.Job.ID {
			if calls != 0 || response.Code == http.StatusOK || strings.Contains(response.Body.String(), `"value"`) {
				t.Fatal("mismatched service binding reached a secret backend")
			}
			for _, route := range routes.endpoints {
				target := strings.NewReplacer("{name}", "TOKEN", "{job}", "producer", "{run}", "child", "{cleanup}", "1").Replace(route.Pattern)
				response := registeredServiceRequest(t, routes, auth, route.Method, target, `{}`)
				if response.Code != http.StatusConflict || calls != 0 {
					t.Fatal("mismatched process binding reached a service handler")
				}
			}
		} else if calls != 1 || response.Code != http.StatusOK {
			t.Fatal("bound service failed to read its declared secret")
		}
	})
}

func TestChildRequestRejectsSecretsInEveryOutboundField(t *testing.T) {
	routes, service, auth := serviceFixture(t)
	if err := service.Masks.registerValue("private-secret"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.StartChild = func(context.Context, capability.Authorized, ChildRequest) (string, error) {
		calls++
		return "child", nil
	}
	for _, body := range []string{
		`{"pipeline":"private-secret"}`,
		`{"pipeline":"build","args":{"x":"private-secret"}}`,
		`{"pipeline":"build","repo":"https://private-secret@example.com/repo"}`,
		`{"pipeline":"build","branch":"private-secret"}`,
	} {
		response := registeredServiceRequest(t, routes, auth, http.MethodPost, "/node/v1/children", body)
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "private-secret") {
			t.Errorf("child secret escaped: %d %s", response.Code, response.Body)
		}
	}
	if calls != 0 {
		t.Fatalf("%d secret-bearing children left the host", calls)
	}
}
