package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
)

type policyAuth struct {
	principal capability.Principal
	err       error
	order     *[]string
}

func (a policyAuth) Authenticate(_ context.Context, p capability.Presentation, route capability.RoutePolicy) (capability.Authenticated, error) {
	*a.order = append(*a.order, "authenticate")
	if p.Raw == "" {
		return capability.Authenticated{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "missing credential"}
	}
	return capability.Authenticated{Principal: a.principal, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, a.err
}

type policyBindings struct {
	order *[]string
	err   error
}

func (b policyBindings) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	*b.order = append(*b.order, "authorize")
	return b.err
}

func TestPolicyRouterOrderingAndErrorBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                string
		grant               capability.Grant
		authErr, handlerErr error
		want                int
		code                apierr.Code
		order               []string
	}{
		{"allowed", capability.RunsRead, nil, nil, 204, "", []string{"decode", "authenticate", "authorize", "handler"}},
		{"live denial", capability.NodesClaim, nil, nil, 403, apierr.Forbidden, []string{"decode", "authenticate"}},
		{"dead credential", capability.RunsRead, &apierr.Error{Code: apierr.Unauthenticated, Message: "not live"}, nil, 401, apierr.Unauthenticated, []string{"decode", "authenticate"}},
		{"transaction race", capability.RunsRead, nil, &apierr.Error{Code: apierr.LockHeld, Message: "lease changed"}, 409, apierr.LockHeld, []string{"decode", "authenticate", "authorize", "handler"}},
		{"store failure", capability.RunsRead, errors.New("private store details"), nil, 500, apierr.Internal, []string{"decode", "authenticate"}},
		{"unknown error", capability.RunsRead, &apierr.Error{Code: "private_code", Message: "private details"}, nil, 500, apierr.Internal, []string{"decode", "authenticate"}},
		{"unencodable error", capability.RunsRead, &apierr.Error{Code: apierr.Internal, Details: map[string]any{"bad": make(chan int)}}, nil, 500, apierr.Internal, []string{"decode", "authenticate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			p := capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{
				Team: "team", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}, Audience: capability.Controller,
				Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{tc.grant},
			}}
			mux := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: policyAuth{p, tc.authErr, &order}, Bindings: policyBindings{order: &order}})
			err := mux.Register(controller.Endpoint{
				Method: "GET", Pattern: "/api/v1/runs/{run}", Authentication: capability.AuthCapability,
				Decode: func(r *http.Request) (capability.RequestEvidence, error) {
					order = append(order, "decode")
					if r.PathValue("run") != "run-1" {
						t.Fatal("decoder ran before router match")
					}
					return capability.RequestEvidence{Target: capability.Target{Team: "team", RunID: r.PathValue("run")}}, nil
				},
				Handle: func(w http.ResponseWriter, _ *http.Request, a capability.Authorized) error {
					order = append(order, "handler")
					if a.Target.RunID != "run-1" {
						t.Fatal("handler lost authorized target")
					}
					if tc.handlerErr == nil {
						w.WriteHeader(204)
					}
					return tc.handlerErr
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/api/v1/runs/run-1", nil)
			req.Header.Set("Authorization", "Bearer synthetic")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.want || !reflect.DeepEqual(order, tc.order) {
				t.Fatalf("response %d %s; order %v, want %v", w.Code, w.Body.String(), order, tc.order)
			}
			if tc.code != "" {
				assertPolicyError(t, w, tc.code)
			}
		})
	}
}

func TestPolicyRouterResolvesTeamLocalTargets(t *testing.T) {
	for _, tc := range []struct {
		name, team, targetTeam string
		status                 int
	}{
		{"active team", "team", "", 204},
		{"explicit team", "team", "team", 204},
		{"foreign team", "team", "foreign", 403},
		{"no active team", "", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			p := capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{
				Team: tc.team, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}, Audience: capability.Controller,
				Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead},
			}}
			router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: policyAuth{p, nil, &order}, Bindings: policyBindings{order: &order}})
			if err := router.Register(controller.Endpoint{
				Method: "GET", Pattern: "/api/v1/tokens/{token}", Authentication: capability.AuthCapability,
				Decode: func(r *http.Request) (capability.RequestEvidence, error) {
					return capability.RequestEvidence{Target: capability.Target{Team: tc.targetTeam, TokenPrefix: r.PathValue("token")}}, nil
				},
				Handle: func(w http.ResponseWriter, _ *http.Request, a capability.Authorized) error {
					if a.Target.Team != "team" || a.Target.TokenPrefix != "target" {
						t.Fatalf("target = %+v", a.Target)
					}
					w.WriteHeader(204)
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/api/v1/tokens/target?team=forged", nil)
			r.Header.Set("Authorization", "Bearer synthetic")
			r.Header.Set("X-Sparkwing-Team", "forged")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d %s, want %d", w.Code, w.Body.String(), tc.status)
			}
		})
	}
}

func TestPolicyRouterMissingRoutesAndPolicies(t *testing.T) {
	mux := controller.NewPolicyRouter(controller.RouteAuthorization{})
	called := false
	if err := mux.Register(controller.Endpoint{
		Method: "GET", Pattern: "/without-policy", Authentication: capability.AuthCapability,
		Handle: func(http.ResponseWriter, *http.Request, capability.Authorized) error { called = true; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := mux.Validate(); err == nil {
		t.Fatal("missing policy passed registration validation")
	}
	for path, code := range map[string]apierr.Code{"/without-policy": apierr.PolicyMissing, "/absent": apierr.NotFound} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		assertPolicyError(t, w, code)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/without-policy", nil))
	assertPolicyError(t, w, apierr.MethodNotAllowed)
	if called {
		t.Fatal("unprotected handler ran")
	}
}

func TestPolicyRouterExternalProofCannotBePublic(t *testing.T) {
	called := false
	mux := controller.NewPolicyRouter(controller.RouteAuthorization{})
	if err := mux.Register(controller.Endpoint{
		Method: "POST", Pattern: "/webhooks/stripe", Authentication: capability.AuthPublic,
		Handle: func(http.ResponseWriter, *http.Request, capability.Authorized) error { called = true; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/webhooks/stripe", nil))
	assertPolicyError(t, w, apierr.PolicyMissing)
	if called {
		t.Fatal("payment callback bypassed proof verification")
	}
}

func TestPolicyRouterExternalProofRunsBeforeHandler(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			var order []string
			mux := controller.NewPolicyRouter(controller.RouteAuthorization{})
			if err := mux.Register(controller.Endpoint{
				Method: "POST", Pattern: "/webhooks/stripe", Authentication: capability.AuthPaymentWebhook,
				Verify: func(*http.Request) error {
					order = append(order, "proof")
					if !valid {
						return &apierr.Error{Code: apierr.Unauthenticated, Message: "invalid proof"}
					}
					return nil
				},
				Handle: func(w http.ResponseWriter, _ *http.Request, _ capability.Authorized) error {
					order = append(order, "handler")
					w.WriteHeader(204)
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("POST", "/webhooks/stripe", nil))
			if valid {
				if w.Code != 204 || !reflect.DeepEqual(order, []string{"proof", "handler"}) {
					t.Fatalf("valid proof: %d, %v", w.Code, order)
				}
			} else {
				assertPolicyError(t, w, apierr.Unauthenticated)
				if !reflect.DeepEqual(order, []string{"proof"}) {
					t.Fatal(order)
				}
			}
		})
	}
}

func TestPolicyRouterSessionCSRFAndBearerPrecedence(t *testing.T) {
	for _, mode := range []string{"missing verifier", "invalid csrf", "valid", "malformed bearer"} {
		t.Run(mode, func(t *testing.T) {
			var order []string
			principal := capability.Principal{Class: capability.CredentialConsole, Capability: capability.Capability{
				Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"},
				Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorConsole},
			}}
			a := controller.RouteAuthorization{
				Authenticator: policyAuth{principal: principal, order: &order}, Bindings: policyBindings{order: &order},
				Identify: func(*http.Request) (capability.Presentation, error) {
					order = append(order, "session")
					return capability.Presentation{Class: capability.CredentialConsole, Raw: "synthetic-session"}, nil
				},
			}
			if mode != "missing verifier" {
				a.CheckCSRF = func(*http.Request, capability.Principal) error {
					order = append(order, "csrf")
					if mode == "invalid csrf" {
						return &apierr.Error{Code: apierr.Forbidden, Message: "CSRF validation failed", Details: map[string]any{"reason": apierr.ReasonReauthRequired}}
					}
					return nil
				}
			}
			mux := controller.NewPolicyRouter(a)
			if err := mux.Register(controller.Endpoint{
				Method: "POST", Pattern: "/api/v1/operator/teams/{team}/trust", Authentication: capability.AuthCapability,
				Decode: func(*http.Request) (capability.RequestEvidence, error) { return capability.RequestEvidence{}, nil },
				Handle: func(w http.ResponseWriter, _ *http.Request, _ capability.Authorized) error {
					order = append(order, "handler")
					w.WriteHeader(204)
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/v1/operator/teams/team/trust", nil)
			if mode == "malformed bearer" {
				req.Header.Set("Authorization", "Bearer")
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			switch mode {
			case "valid":
				if w.Code != 204 || !reflect.DeepEqual(order, []string{"session", "authenticate", "csrf", "authorize", "handler"}) {
					t.Fatalf("response=%d order=%v", w.Code, order)
				}
			case "missing verifier":
				assertPolicyError(t, w, apierr.PolicyMissing)
			case "invalid csrf":
				assertPolicyError(t, w, apierr.Forbidden)
			case "malformed bearer":
				assertPolicyError(t, w, apierr.Unauthenticated)
				if len(order) != 0 {
					t.Fatalf("invalid bearer fell back to session: %v", order)
				}
			}
		})
	}
}

func TestPolicyRouterDeadCredentialHidesDecoderError(t *testing.T) {
	var order []string
	mux := controller.NewPolicyRouter(controller.RouteAuthorization{
		Authenticator: policyAuth{order: &order, err: &apierr.Error{Code: apierr.Unauthenticated, Message: "unknown credential"}},
		Bindings:      policyBindings{order: &order},
	})
	if err := mux.Register(controller.Endpoint{
		Method: "GET", Pattern: "/api/v1/runs/{run}", Authentication: capability.AuthCapability,
		Decode: func(*http.Request) (capability.RequestEvidence, error) {
			return capability.RequestEvidence{}, &apierr.Error{Code: apierr.NotFound, Message: "missing resource"}
		},
		Handle: func(http.ResponseWriter, *http.Request, capability.Authorized) error {
			t.Fatal("handler ran")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/runs/run", nil)
	r.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	assertPolicyError(t, w, apierr.Unauthenticated)
}

func assertPolicyError(t *testing.T, w *httptest.ResponseRecorder, code apierr.Code) {
	t.Helper()
	var envelope apierr.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if w.Code != apierr.Status(code) || envelope.Error == nil || envelope.Error.Code != code || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = %d %s, want %s", w.Code, w.Body.String(), code)
	}
	if code == apierr.Internal && envelope.Error.Message != "request failed" {
		t.Fatalf("internal details escaped: %s", w.Body.String())
	}
}
