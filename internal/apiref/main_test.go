package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestReferenceKeepsCompleteAuthorizationAlternatives(t *testing.T) {
	route := capability.RoutePolicy{
		Method: "POST", Path: "/api/v1/runs/{run}/jobs/{job}/transition", Audience: capability.Controller,
		Owner: capability.OwnerRun, Authentication: capability.AuthCapability, Retry: capability.RetryExactBody,
		Access: []capability.AccessRule{
			{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.NodeResult, capability.DataWrite}, Binding: capability.BindOwnNode, Liveness: capability.LiveResult},
			{Credential: capability.CredentialPublisher, Grants: []capability.Grant{capability.DataWrite}, Binding: capability.BindData, Liveness: capability.LiveCredential},
		},
	}
	var out bytes.Buffer
	if err := writeReference(&out, []capability.RoutePolicy{route}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| `POST` | `/api/v1/runs/{run}/jobs/{job}/transition` | `controller` | `run` | `capability` | `exact_body` |",
		"`awarded_or_delegated_claim` / `node.result + data.write` / `ownNode` / `result`<br>`local_run_publisher` / `data.write` / `data` / `credential`",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("policy missing: %s", want)
		}
	}
}

func TestReferenceIncludesEveryFinalRouteOnce(t *testing.T) {
	var out bytes.Buffer
	if err := writeReference(&out, capability.Routes()); err != nil {
		t.Fatal(err)
	}
	for _, route := range capability.Routes() {
		selector := "| `" + route.Method + "` | `" + route.Path + "` |"
		if strings.Count(out.String(), selector) != 1 {
			t.Fatalf("route count for %s", selector)
		}
	}
	for _, removed := range []string{"/api/v1/triggers", "/api/v1/nodes/claim", "/operator/cache", "admin` satisfies"} {
		if strings.Contains(out.String(), removed) {
			t.Fatalf("removed policy remains: %s", removed)
		}
	}
}

func TestReferenceMatchesPublishedDocument(t *testing.T) {
	var out bytes.Buffer
	if err := writeReference(&out, capability.Routes()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "docs", "api-reference.md")
	published, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, out.Bytes()) {
		t.Fatal("regenerate docs/api-reference.md with internal/apiref")
	}
}

type failedReferenceWriter struct{}

func (failedReferenceWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestReferenceReturnsOutputFailure(t *testing.T) {
	if err := writeReference(failedReferenceWriter{}, capability.Routes()); err == nil {
		t.Fatal("lost output error")
	}
}
