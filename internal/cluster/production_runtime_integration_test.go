//go:build rewrite_integration

package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const productionHTTPBoundary = "production HTTP boundary"

func TestProductionRunnerOwnerReachesNativeHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "state"))
	raw := productionTestBearer("owner")
	binding := capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "runner", Generation: 1}
	principal := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, Capability: capability.Capability{
		Prefix: "owner", Digest: sha256.Sum256([]byte(raw)), Team: "team", Audience: capability.Controller,
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Issuance: capability.Award,
		Unit: capability.UnitTrigger, UnitID: "trigger", Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect,
		Binding: binding, Grants: []capability.Grant{capability.TriggerHost}, Data: &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:42", RunID: "run"},
	}}
	now := time.Now().UTC()
	request := runspec.SealedRequest{RunID: "run", Hash: "request-hash", Request: runspec.Request{
		Team: "team", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:42"}, SHA: strings.Repeat("a", 40)},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/project", PipelineDir: ".sparkwing"}},
	}}
	unit := controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "trigger", Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect, RunID: "run", Binding: binding, Capability: raw, Request: request}
	connection := client.EngineOptions{URL: "http://controller.invalid", Bearer: fixedBearer(raw), HTTP: &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+raw {
			t.Fatal("native binding lost the owner award")
		}
		var response any
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/runs/run/local-host/authentication":
			response = localhostwire.LocalAuthentication{Snapshot: capability.AuthenticationSnapshot{
				Now: now, Authorities: []capability.AuthorityState{{Principal: principal, MembershipValid: true}},
				Binding: capability.BindingState{Current: binding, LeaseExpiresAt: now.Add(time.Hour)},
			}, Digests: [][32]byte{principal.Capability.Digest}}
		case "GET /api/v1/runs/run/local-host/data-authority":
			response = localhostwire.LocalDataAuthority{Authority: blob.Authority{CapabilityRef: "owner", Principal: "runner", Repo: "gh:42"}}
		case "GET /api/v1/runs/run/local-host/snapshot":
			response = store.RunSnapshot{Run: store.RunRow{ID: "run", Team: "team", Request: request}}
		default:
			t.Fatalf("unexpected native binding request: %s %s", r.Method, r.URL)
		}
		body, err := json.Marshal(response)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, err
	})}}
	controller, err := client.NewEngineClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	services, _, err := productionHosting(t.TempDir(), "v1.2.3", connection).Bind(t.Context(), unit, controller)
	if services.Close != nil {
		t.Cleanup(func() {
			if err := services.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	var socket *net.OpError
	if err != nil && errors.As(err, &socket) && socket.Op == "listen" && errors.Is(err, syscall.EPERM) {
		t.Skipf("native host reached the sandbox socket boundary: %v", err)
	}
	if err != nil {
		t.Fatalf("production owner did not construct native services: %v", err)
	}
}

func TestInstalledRunnerRuntimeComposition(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_RUNNER_ENTRYPOINT") == "1" {
		http.DefaultTransport = productionTransport(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("%s: %s %s%s", productionHTTPBoundary, r.Method, r.URL.Host, r.URL.Path)
		})
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"sparkwing-runner"}, os.Args[i+1:]...)
				MainWithVersion("v1.2.3")
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	options, award := bootstrapPlanFile(t)
	award.Source = source.Spec{RepoKey: "gh:42", RemoteURL: "https://github.com/acme/project", SHA: strings.Repeat("a", 40), PipelineDir: ".sparkwing", DefaultBranch: "main"}
	metadata, err := json.Marshal(award)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(options.BootstrapFile), "award.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	credentials := runnerCredentials(t)
	config := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(config, []byte("runner:\n  controller: https://controller.example\n  repos: [github.com/team/*]\n  credentials_dir: "+credentials+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	commands := map[string][]string{
		"start":        {"--config=" + config},
		"warm":         {"--controller=https://controller.example", "--credentials-dir=" + credentials},
		"launch":       {"--controller=https://controller.example", "--image=" + image, "--docker-image=" + image, "--cpu-ceiling=1", "--memory-ceiling=1Gi", "--credentials-dir=" + credentials},
		"fetch-source": bootstrapArguments(options),
		"run-node":     bootstrapArguments(options),
	}
	operations := map[string]string{
		"start":        "GET sparkwing.dev/versions.json",
		"warm":         "GET sparkwing.dev/versions.json",
		"fetch-source": "POST controller.example/api/v1/runs/run/source-credential",
		"run-node":     "POST controller.example/api/v1/claims/exchange",
	}
	for name, args := range commands {
		t.Run(name, func(t *testing.T) {
			child := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestInstalledRunnerRuntimeComposition$", "--", name}, args...)...)
			child.Env = append(os.Environ(), "SPARKWING_TEST_RUNNER_ENTRYPOINT=1", "HOME="+directory, "SPARKWING_HOME="+filepath.Join(directory, "state"), "XDG_CACHE_HOME="+filepath.Join(directory, "cache"))
			output, err := child.CombinedOutput()
			t.Logf("entrypoint result: %s", output)
			if err == nil {
				t.Fatal("runner exited without attempting its configured production operation")
			}
			for _, missing := range []string{"runner runtime is unavailable", "launcher runtime is unavailable", "requires the source-access service binding", "requires the shared host service binding"} {
				if strings.Contains(string(output), missing) {
					t.Fatalf("installed entrypoint has no production composition: %s", output)
				}
			}
			want := productionHTTPBoundary
			if operation, ok := operations[name]; ok {
				want += ": " + operation
			}
			if !strings.Contains(string(output), want) {
				t.Fatalf("installed entrypoint did not reach its production HTTP operation: %s", output)
			}
		})
	}
}
