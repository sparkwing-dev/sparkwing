package controller_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestFinalCredentialRouteDeclarations(t *testing.T) {
	expected := credentialRouteExpectations()
	if err := compareCredentialRoutes(expected, capability.Routes); err != nil {
		t.Fatal(err)
	}
}

func TestFinalCredentialServingRegistrations(t *testing.T) {
	for _, separateMetrics := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate_metrics=%t", separateMetrics), func(t *testing.T) {
			actual := servingRegistrations(t, separateMetrics)
			if err := compareServingRegistrations(credentialRouteExpectations(), actual); err != nil {
				t.Fatal(err)
			}
			if err := capability.ValidateRoutes(actual); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func servingRegistrations(t *testing.T, separateMetrics bool) []string {
	t.Helper()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	server := controller.New(nil, nil).WithDataObjects(objects).WithDashboard(controller.Dashboard{})
	if separateMetrics {
		server.WithMetricsAddr("127.0.0.1:0")
	}
	actual := server.Registrations()
	local := httptest.NewRecorder()
	server.HTTP().API.ServeHTTP(local, httptest.NewRequest("POST", "/api/v1/local/doctor", nil))
	if local.Code != http.StatusInternalServerError || !strings.Contains(local.Body.String(), `"code":"policy_missing"`) {
		t.Fatalf("local doctor registration: HTTP %d %s", local.Code, local.Body)
	}
	actual = append(actual, "POST /api/v1/local/doctor")
	processes := controller.NewPolicyRouter(controller.RouteAuthorization{})
	if err := nodehost.RegisterProcessServices(processes, func(context.Context, capability.Authorized) (*nodehost.ProcessServices, error) {
		return nil, fmt.Errorf("route inventory must not resolve a process")
	}); err != nil {
		t.Fatal(err)
	}
	return append(actual, processes.Registrations()...)
}

func compareServingRegistrations(expected []credentialRouteExpectation, actual []string) error {
	want := make(map[string]bool, len(expected))
	for _, route := range expected {
		want[route.Method+" "+route.Path] = true
	}
	seen := make(map[string]bool, len(actual))
	var problems []string
	for _, key := range actual {
		if seen[key] {
			problems = append(problems, "duplicate registration: "+key)
		}
		seen[key] = true
		if !want[key] {
			problems = append(problems, "unexpected registration: "+key)
		}
	}
	for key := range want {
		if !seen[key] {
			problems = append(problems, "missing registration: "+key)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	return fmt.Errorf("serving route inventory differs:\n%s", strings.Join(problems, "\n"))
}

func TestFinalCredentialServingCoverageRejectsDriftProperty(t *testing.T) {
	actual := servingRegistrations(t, false)
	property := func(index uint32, suffix string) bool {
		i := int(index % uint32(len(actual)))
		missing := append(slices.Clone(actual[:i]), actual[i+1:]...)
		extra := "POST /unclassified/" + fmt.Sprintf("%x", suffix)
		cases := []struct {
			actual []string
			want   string
		}{
			{missing, "missing registration: " + actual[i]},
			{append(slices.Clone(actual), actual[i]), "duplicate registration: " + actual[i]},
			{append(slices.Clone(actual), extra), "unexpected registration: " + extra},
		}
		for _, change := range cases {
			err := compareServingRegistrations(credentialRouteExpectations(), change.actual)
			if err == nil || !strings.Contains(err.Error(), change.want) {
				t.Logf("drift %q: %v", change.want, err)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func compareCredentialRoutes(expected []credentialRouteExpectation, actual []capability.RoutePolicy) error {
	want := make(map[string]credentialRouteExpectation, len(expected))
	for _, row := range expected {
		key := row.Method + " " + row.Path
		if _, exists := want[key]; exists {
			return fmt.Errorf("duplicate expected route %s", key)
		}
		want[key] = row
	}
	seen := make(map[string]bool, len(actual))
	for _, row := range actual {
		key := row.Method + " " + row.Path
		if seen[key] {
			return fmt.Errorf("duplicate route %s", key)
		}
		seen[key] = true
		expected, ok := want[key]
		if !ok {
			return fmt.Errorf("unexpected route %s", key)
		}
		if string(row.Audience) != expected.Audience || string(row.Owner) != expected.Owner || string(row.Authentication) != expected.Authentication {
			return fmt.Errorf("route %s: audience/owner/authentication changed", key)
		}
		alternatives := make([]string, 0, len(row.Access))
		for _, access := range row.Access {
			grants := make([]string, len(access.Grants))
			for i, grant := range access.Grants {
				grants[i] = string(grant)
			}
			slices.Sort(grants)
			alternatives = append(alternatives, strings.Join([]string{string(access.Credential), strings.Join(grants, ","), string(access.Binding), string(access.Liveness)}, "|"))
		}
		wanted := slices.Clone(expected.Alternatives)
		slices.Sort(wanted)
		slices.Sort(alternatives)
		if !slices.Equal(wanted, alternatives) {
			return fmt.Errorf("route %s: alternatives = %v, want %v", key, alternatives, wanted)
		}
	}
	for key := range want {
		if !seen[key] {
			return fmt.Errorf("missing route %s", key)
		}
	}
	return nil
}

func TestFinalCredentialRouteCoverageRejectsDrift(t *testing.T) {
	intakeIndex := slices.IndexFunc(capability.Routes, func(route capability.RoutePolicy) bool {
		return route.Method == "POST" && route.Path == "/api/v1/runs"
	})
	if intakeIndex < 0 {
		t.Fatal("missing run intake route")
	}
	tests := map[string]func([]capability.RoutePolicy) []capability.RoutePolicy{
		"missing route": func(r []capability.RoutePolicy) []capability.RoutePolicy { return r[1:] },
		"extra route": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			extra := r[intakeIndex]
			extra.Path = "/unexpected"
			return append(r, extra)
		},
		"duplicate route": func(r []capability.RoutePolicy) []capability.RoutePolicy { return append(r, r[intakeIndex]) },
		"missing alternative": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access = r[intakeIndex].Access[1:]
			return r
		},
		"extra alternative": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access = append(r[intakeIndex].Access, r[intakeIndex].Access[0])
			return r
		},
		"missing grant": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access[0].Grants = nil
			return r
		},
		"extra grant": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access[0].Grants = append(r[intakeIndex].Access[0].Grants, capability.TeamAdmin)
			return r
		},
		"grant pairing": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access[0].Grants, r[intakeIndex].Access[1].Grants = r[intakeIndex].Access[1].Grants, r[intakeIndex].Access[0].Grants
			return r
		},
		"liveness pairing": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access[0].Liveness, r[intakeIndex].Access[1].Liveness = r[intakeIndex].Access[1].Liveness, r[intakeIndex].Access[0].Liveness
			return r
		},
		"binding": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access[1].Binding = capability.BindNone
			return r
		},
		"credential": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Access[1].Credential = capability.CredentialAny
			return r
		},
		"audience": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Audience = capability.NodeHost
			return r
		},
		"owner": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Owner = capability.OwnerNone
			return r
		},
		"authentication": func(r []capability.RoutePolicy) []capability.RoutePolicy {
			r[intakeIndex].Authentication = capability.AuthPublic
			return r
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			routes := cloneCredentialRoutes()
			if err := compareCredentialRoutes(credentialRouteExpectations(), mutate(routes)); err == nil {
				t.Fatal("accepted changed permission contract")
			}
		})
	}
}

func TestFinalCredentialRouteAlternativeOrder(t *testing.T) {
	routes := cloneCredentialRoutes()
	slices.Reverse(routes)
	for i := range routes {
		slices.Reverse(routes[i].Access)
	}
	if err := compareCredentialRoutes(credentialRouteExpectations(), routes); err != nil {
		t.Fatal(err)
	}
}

func cloneCredentialRoutes() []capability.RoutePolicy {
	routes := slices.Clone(capability.Routes)
	for i := range routes {
		routes[i].Access = slices.Clone(routes[i].Access)
		for j := range routes[i].Access {
			routes[i].Access[j].Grants = slices.Clone(routes[i].Access[j].Grants)
		}
	}
	return routes
}
