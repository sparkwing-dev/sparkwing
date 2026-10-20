package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestControllerRegistersDeclaredLogSubmissions(t *testing.T) {
	server := New(nil, nil)
	for _, path := range []string{
		"/api/v1/claims/claim/logs",
		"/api/v1/claims/claim/log-segments",
		"/api/v1/claims/claim/log-manifest",
		"/api/v1/runs/run/jobs/job/log-segments",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, nil)
			if !server.RegisteredRoute(request) {
				t.Fatal("declared log submission returns a route miss")
			}
		})
	}
}

func TestMetricsServingRouterRequiresCredential(t *testing.T) {
	property := func(input string, allowed bool) bool {
		server := New(nil, nil).WithMetricsAddr("127.0.0.1:0").WithAuthorization(RouteAuthorization{
			Authenticator: metricsRouteAuthority{allowed: allowed}, Bindings: metricsRouteAuthority{},
		})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if input != "" {
			request.Header.Set("Authorization", "Bearer credential")
		}
		server.metricsServer().Handler.ServeHTTP(response, request)
		want := http.StatusForbidden
		if input == "" {
			want = http.StatusUnauthorized
		} else if allowed {
			want = http.StatusOK
		}
		if response.Code != want {
			t.Logf("metrics: got %d, want %d", response.Code, want)
			return false
		}
		return true
	}
	if !property("", false) {
		t.Fatal("metrics accepted an anonymous request")
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

type metricsRouteAuthority struct{ allowed bool }

func (a metricsRouteAuthority) Authenticate(_ context.Context, _ capability.Presentation, route capability.RoutePolicy) (capability.Authenticated, error) {
	grant := capability.RunsRead
	if a.allowed {
		grant = capability.OperatorLimits
	}
	return capability.Authenticated{
		Principal: capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{
			Audience: capability.Controller, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"},
			Grants: []capability.Grant{grant}, Binding: capability.Binding{Kind: capability.BindingNone},
		}},
		Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}},
	}, nil
}

func (metricsRouteAuthority) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return nil
}
