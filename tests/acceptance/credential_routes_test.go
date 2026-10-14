package acceptance_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestFinalCredentialRouteDeclarations(t *testing.T) {
	expected := acceptance.CredentialRoutes()
	if err := compareCredentialRoutes(expected, capability.Routes); err != nil {
		t.Fatal(err)
	}
}

func compareCredentialRoutes(expected []acceptance.CredentialRouteExpectation, actual []capability.RoutePolicy) error {
	want := make(map[string]acceptance.CredentialRouteExpectation, len(expected))
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
			if err := compareCredentialRoutes(acceptance.CredentialRoutes(), mutate(routes)); err == nil {
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
	if err := compareCredentialRoutes(acceptance.CredentialRoutes(), routes); err != nil {
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
