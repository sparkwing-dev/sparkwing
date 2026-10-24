package nodehost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type routeResolver func(context.Context, string) (string, bool, error)

func (f routeResolver) Resolve(ctx context.Context, name string) (string, bool, error) {
	return f(ctx, name)
}

func registeredRouter(t testing.TB) (*server, http.Handler) {
	t.Helper()
	s := start(t)
	return s, s.handler
}

func routedRequest(t testing.TB, handler http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+bearer)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestProductionRouterCanonicalRefusalsAndHardCut(t *testing.T) {
	s, handler := registeredRouter(t)
	masker := secrets.NewMasker()
	service := newService(t)
	service.RunID, service.Job.ID = "run", "job"
	service.Masks = NewMaskRegistry(masker)
	service.Secret = func(context.Context, capability.Authorized, string) (string, bool, error) {
		t.Fatal("undeclared secret reached resolver")
		return "", false, nil
	}
	service.Latest = func(context.Context, capability.Authorized, string, string, time.Duration) (ProcessOutput, error) {
		t.Fatal("malformed latest reached backend")
		return ProcessOutput{}, nil
	}
	bearer, revoke, err := s.Admit(routeNode{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	for _, test := range []struct {
		method, path, body string
		code               apierr.Code
		reason             apierr.Reason
	}{
		{http.MethodGet, "/node/v1/secrets/undeclared", "", apierr.Forbidden, apierr.ReasonSecretUndeclared},
		{http.MethodGet, "/node/v1/nodes/job/output", "", apierr.NotFound, ""},
		{http.MethodGet, "/node/v1/masks", "", apierr.MethodNotAllowed, ""},
		{http.MethodGet, "/node/v1/pipelines/build/latest?node=build", "", apierr.BadRequest, ""},
		{http.MethodPost, "/node/v1/masks", `{"value":"dynamic-credential"}`, "", ""},
		{http.MethodPost, "/node/v1/annotations", `{"message":"line","step":"undeclared"}`, apierr.UnknownStep, ""},
		{http.MethodPost, "/node/v1/summary", `{"markdown":"line","step":"undeclared"}`, apierr.UnknownStep, ""},
	} {
		response := routedRequest(t, handler, test.method, test.path, bearer, test.body)
		if test.code == "" {
			if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "{}" {
				t.Fatalf("ack=%d %s", response.Code, response.Body)
			}
			continue
		}
		var envelope apierr.Envelope
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error == nil || envelope.Error.Code != test.code || response.Code != apierr.Status(test.code) {
			t.Fatalf("%s %s: %d %s", test.method, test.path, response.Code, response.Body)
		}
		if test.reason != "" && envelope.Error.Details["reason"] != string(test.reason) {
			t.Fatalf("refusal lost reason: %+v", envelope.Error)
		}
	}
	if masker.Mask("dynamic-credential") != "***" {
		t.Fatal("mask was acknowledged before registration")
	}
}

func FuzzProductionRouterBindsSecretReads(f *testing.F) {
	f.Add("declared", "other", "run-a", "run-b")
	f.Add("TOKEN", "../TOKEN", "run:one", "run/two")
	f.Fuzz(func(t *testing.T, declared, requested, runA, runB string) {
		if declared == "" || requested == "" || len(declared) > 100 || len(requested) > 100 || strings.ContainsAny(declared+requested, "\x00\r\n") || (!utf8.ValidString(runA) || !utf8.ValidString(runB)) {
			t.Skip()
		}
		s, handler := registeredRouter(t)
		var reads atomic.Int32
		owner := routeNode{RunID: "a:" + runA, NodeID: "job", Secrets: []string{declared}, Masker: secrets.NewMasker(), Resolver: routeResolver(func(_ context.Context, name string) (string, bool, error) {
			reads.Add(1)
			if name != declared {
				t.Error("resolver reached with undeclared name")
			}
			return "private-value", true, nil
		})}
		bearer, revoke, err := s.Admit(owner)
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		foreign, foreignRevoke, err := s.Admit(routeNode{RunID: "b:" + runB, NodeID: "job", Masker: secrets.NewMasker(), Resolver: owner.Resolver})
		if err != nil {
			t.Fatal(err)
		}
		defer foreignRevoke()
		path := "/node/v1/secrets/" + strings.ReplaceAll(url.PathEscape(requested), ".", "%2E")
		foreignResponse := routedRequest(t, handler, http.MethodGet, path, foreign, "")
		if foreignResponse.Code != http.StatusForbidden || reads.Load() != 0 {
			t.Fatalf("foreign read reached backend: %d %s", foreignResponse.Code, foreignResponse.Body)
		}
		response := routedRequest(t, handler, http.MethodGet, path, bearer, "")
		if requested != declared && (response.Code != http.StatusForbidden || reads.Load() != 0) {
			t.Fatalf("undeclared read reached backend: %d %s", response.Code, response.Body)
		}
		if requested == declared && (response.Code != http.StatusOK || reads.Load() != 1) {
			t.Fatalf("declared read did not use its own backend: %d %s", response.Code, response.Body)
		}
		revoke()
		response = routedRequest(t, handler, http.MethodGet, path, bearer, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("revoked grant remained usable: %d %s", response.Code, response.Body)
		}
	})
}

func FuzzProductionRouterBindsImplicitSelectors(f *testing.F) {
	f.Add("job", "foreign", "run")
	f.Add("job", "job", "run")
	f.Add("job", "", "run")
	f.Fuzz(func(t *testing.T, job, selector, run string) {
		if job == "" || run == "" || len(job) > 100 || len(selector) > 100 || len(run) > 100 || (!utf8.ValidString(job) || !utf8.ValidString(selector) || !utf8.ValidString(run)) {
			t.Skip()
		}
		s := start(t)
		service := newService(t)
		service.RunID, service.Job.ID = run, job
		calls := 0
		check := func(auth capability.Authorized) {
			calls++
			if auth.Binding.RunID != run || auth.Binding.NodeID != job || auth.Target.RunID != run || auth.Target.NodeID != job || auth.Target.Team != "team" {
				t.Fatalf("callback escaped its process: %+v", auth)
			}
		}
		service.Summary = func(_ context.Context, auth capability.Authorized, _ nodeproto.SummaryRequest) error {
			check(auth)
			return nil
		}
		service.StartChild = func(_ context.Context, auth capability.Authorized, _ ChildRequest) (string, error) {
			check(auth)
			if auth.Target.BodyRunID != run {
				t.Fatalf("child escaped parent run: %+v", auth.Target)
			}
			return "child", nil
		}
		bearer := admitService(t, s, service)
		for _, call := range []struct {
			path, body string
			status     int
		}{
			{"/node/v1/summary", `{"markdown":"record"}`, http.StatusOK},
			{"/node/v1/children", `{"pipeline":"build"}`, http.StatusCreated},
		} {
			calls = 0
			response := routedRequest(t, s.handler, http.MethodPost, call.path+"?job="+url.QueryEscape(selector), bearer, call.body)
			if selector != "" && selector != job {
				if response.Code != http.StatusForbidden || calls != 0 {
					t.Fatalf("foreign selector reached callback: %d %s calls=%d", response.Code, response.Body, calls)
				}
			} else if response.Code != call.status || calls != 1 {
				t.Fatalf("bound selector refused: %d %s calls=%d", response.Code, response.Body, calls)
			}
		}
	})
}
