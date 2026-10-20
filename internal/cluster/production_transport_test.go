package cluster

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type productionTransport func(*http.Request) (*http.Response, error)

func (f productionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProductionHostingRetainsPrivateTransport(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			boundary := errors.New("private authentication boundary")
			bearer := productionTestBearer("award")
			calls := 0
			transport := &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				want := "/api/v1/runs/run/local-host/authentication"
				if kind == capability.UnitNode {
					want = "/api/v1/runs/run/local-host/nodes/build/authentication"
				}
				if r.Method != "POST" || r.URL.Scheme != "http" || r.URL.Host != "private.invalid" || r.URL.Path != want || r.Header.Get("Authorization") != "Bearer "+bearer {
					t.Errorf("private request lost the award or destination: %s %s", r.Method, r.URL)
				}
				return nil, boundary
			})}
			connection := client.EngineOptions{URL: "http://private.invalid", HTTP: transport, Launcher: true, Bearer: fixedBearer("ambient-root")}
			hosting := productionHostingAt(t.TempDir(), "v1.2.3", connection, "", "", false, procenv.SubmitAccept{}, nil)
			if hosting.HTTP != transport || hosting.ControllerURL != connection.URL {
				t.Fatal("hosting lost its configured transport")
			}
			unit := controllerwire.ClaimedUnit{Unit: kind, RunID: "run", Capability: bearer}
			if kind == capability.UnitNode {
				unit.JobID = "build"
			}
			services, _, err := hosting.Bind(t.Context(), unit, nil)
			if !errors.Is(err, boundary) || calls != 1 || services.Close != nil {
				t.Fatalf("private state escaped the configured transport: calls=%d error=%v", calls, err)
			}
			if token, err := connection.Bearer(t.Context()); err != nil || token != "ambient-root" {
				t.Fatal("binding changed its caller's credential")
			}
		})
	}
}

func FuzzProductionHostingUsesOnlyAwardedCredential(f *testing.F) {
	f.Add("one", "ambient", false)
	f.Add("two", "bootstrap", true)
	f.Fuzz(func(t *testing.T, nonce, ambient string, node bool) {
		boundary := errors.New("private authentication boundary")
		bearer := productionTestBearer(nonce)
		calls := 0
		httpClient := &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Header.Get("Authorization") != "Bearer "+bearer {
				t.Fatal("private request used ambient credential authority")
			}
			return nil, boundary
		})}
		hosting := productionHostingAt(t.TempDir(), "v1.2.3", client.EngineOptions{URL: "http://private.invalid", HTTP: httpClient, Bearer: fixedBearer(ambient)}, "", "", false, procenv.SubmitAccept{}, nil)
		unit := controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, RunID: "run", Capability: bearer}
		if node {
			unit.Unit, unit.JobID = capability.UnitNode, "build"
		}
		if _, _, err := hosting.Bind(t.Context(), unit, nil); !errors.Is(err, boundary) || calls != 1 {
			t.Fatalf("award did not reach private authentication: calls=%d error=%v", calls, err)
		}
	})
}

func productionTestBearer(value string) string {
	digest := sha256.Sum256([]byte(value))
	return capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(digest[:])
}

func TestActionsSourceUsesOnlyWorkflowRepositoryCredential(t *testing.T) {
	access := &githubActionsSource{repository: "gh:42", token: "job-github-token"}
	spec := source.Spec{RepoKey: "gh:42", RemoteURL: "git@github.com:acme/repo.git"}
	got, err := access.GitCredential(t.Context(), spec)
	if err != nil || got.Secret != "job-github-token" || got.Host != "github.com" {
		t.Fatalf("workflow source: %+v %v", got, err)
	}
	for _, foreign := range []source.Spec{{RepoKey: "gh:43", RemoteURL: spec.RemoteURL}, {RepoKey: spec.RepoKey, RemoteURL: "https://other.example/acme/repo"}} {
		if got, err := access.GitCredential(t.Context(), foreign); err == nil || got.Secret != "" {
			t.Fatalf("foreign source credential: %+v %v", got, err)
		}
	}
}
