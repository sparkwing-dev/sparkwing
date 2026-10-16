package cluster

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
			hosting := productionHosting(t.TempDir(), "v1.2.3", connection)
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
		hosting := productionHosting(t.TempDir(), "v1.2.3", client.EngineOptions{URL: "http://private.invalid", HTTP: httpClient, Bearer: fixedBearer(ambient)})
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
