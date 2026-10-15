package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type routeResolver func(context.Context, string) (string, bool, error)

func (f routeResolver) Resolve(ctx context.Context, name string) (string, bool, error) {
	return f(ctx, name)
}

func registeredRouter(t testing.TB) (*Server, http.Handler) {
	t.Helper()
	s := &Server{ctx: t.Context(), logger: slog.New(slog.DiscardHandler), grants: map[[bearerHashSize]byte]*grant{}}
	handler, err := s.routes()
	if err != nil {
		t.Fatal(err)
	}
	return s, handler
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
	bearer, revoke, err := s.Admit(Node{RunID: "run", NodeID: "job", Masker: masker, Resolver: routeResolver(func(context.Context, string) (string, bool, error) {
		t.Fatal("undeclared secret reached resolver")
		return "", false, nil
	})})
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
		{http.MethodGet, "/node/v1/nodes/job/output", "", apierr.UnsupportedRoute, ""},
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
		if declared == "" || requested == "" || len(declared) > 100 || len(requested) > 100 || strings.ContainsAny(declared+requested, "\x00\r\n") {
			t.Skip()
		}
		s, handler := registeredRouter(t)
		var reads atomic.Int32
		owner := Node{RunID: "a:" + runA, NodeID: "job", Secrets: []string{declared}, Masker: secrets.NewMasker(), Resolver: routeResolver(func(_ context.Context, name string) (string, bool, error) {
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
		foreign, foreignRevoke, err := s.Admit(Node{RunID: "b:" + runB, NodeID: "job", Masker: secrets.NewMasker(), Resolver: owner.Resolver})
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
