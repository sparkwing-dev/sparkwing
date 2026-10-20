package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOpenAPIRetainsTheFinalPolicyInventory(t *testing.T) {
	body, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Paths["/api/v1/triggers"] != nil {
		t.Error("removed trigger routes remain in OpenAPI")
	}
	for _, route := range capability.Routes() {
		found := false
		for method, node := range doc.Paths[strings.ReplaceAll(route.Path, "...}", "}")] {
			if method != strings.ToLower(route.Method) {
				continue
			}
			found = true
			var op struct {
				Policy *capability.RoutePolicy `yaml:"x-sparkwing-policy"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatal(err)
			}
			if op.Policy == nil || !samePolicy(*op.Policy, route) {
				t.Errorf("%s %s omits final authorization metadata", route.Method, route.Path)
			}
		}
		if !found {
			t.Errorf("missing %s %s", route.Method, route.Path)
		}
	}
}

func samePolicy(a, b capability.RoutePolicy) bool {
	if a.Method != b.Method || a.Path != b.Path || a.Audience != b.Audience || a.Owner != b.Owner || a.Authentication != b.Authentication || a.Retry != b.Retry || len(a.Access) != len(b.Access) {
		return false
	}
	for i, access := range a.Access {
		other := b.Access[i]
		if access.Credential != other.Credential || access.Binding != other.Binding || access.Liveness != other.Liveness || !slices.Equal(access.Grants, other.Grants) {
			return false
		}
	}
	return true
}

func TestGeneratedSpecMatchesThePublishedContract(t *testing.T) {
	var out bytes.Buffer
	if err := writeSpec(&out, capability.Routes(), wireOperations()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var generated, published any
	if err := yaml.Unmarshal(out.Bytes(), &generated); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(body, &published); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(generated, published) {
		t.Fatal("published OpenAPI differs from shared policy and wire types")
	}
}

func TestSpecRefusesInvalidBindingsBeforeWriting(t *testing.T) {
	route := capability.Routes()[0]
	for _, test := range []struct {
		routes []capability.RoutePolicy
		bodies map[string]wireOperation
	}{
		{routes: []capability.RoutePolicy{route, route}},
		{bodies: map[string]wireOperation{"GET /removed": {}}},
		{routes: []capability.RoutePolicy{{Method: "GET", Path: "/x/{key...}"}, {Method: "GET", Path: "/x/{key}"}}},
		{routes: []capability.RoutePolicy{{Method: "GET", Path: "relative"}}},
	} {
		var out bytes.Buffer
		if err := writeSpec(&out, test.routes, test.bodies); err == nil || out.Len() != 0 {
			t.Fatal("invalid contract emitted a document")
		}
	}
	for _, writer := range []io.Writer{failedWriter{}, shortWriter{}} {
		if err := writeSpec(writer, nil, nil); err == nil {
			t.Fatal("output failure reported success")
		}
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func FuzzPolicyAlternativesRemainWhole(f *testing.F) {
	f.Add("nodes.claim", "triggers.claim", "run", "lease")
	f.Add("quote\"\n:", "雪", "", "a/b~c")
	f.Fuzz(func(t *testing.T, first, second, binding, live string) {
		route := capability.RoutePolicy{Method: "POST", Path: "/api/v1/example", Authentication: capability.AuthCapability, Access: []capability.AccessRule{
			{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.Grant(first), capability.Grant(second)}, Binding: capability.BindingRule(binding), Liveness: capability.Liveness(live)},
			{Credential: capability.CredentialOperator, Grants: []capability.Grant{capability.TeamAdmin}},
		}}
		var out bytes.Buffer
		if err := writeSpec(&out, []capability.RoutePolicy{route}, nil); err != nil {
			t.Fatal(err)
		}
		var document struct {
			Paths map[string]map[string]struct {
				Policy capability.RoutePolicy `yaml:"x-sparkwing-policy"`
			}
		}
		if err := yaml.Unmarshal(out.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		got := document.Paths[route.Path]["post"].Policy
		wantJSON, _ := json.Marshal(route)
		gotJSON, _ := json.Marshal(got)
		if !bytes.Equal(wantJSON, gotJSON) {
			t.Fatal("generated policy merged, lost or rewrote an access alternative")
		}
	})
}
