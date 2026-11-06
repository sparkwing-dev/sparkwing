package runners

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type nativeHostingTransport func(*http.Request) (*http.Response, error)

func (f nativeHostingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeHostingKeepsAwardTransportAndCapturedInputs(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		for _, local := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/local=%v", kind, local), func(t *testing.T) {
				home := t.TempDir()
				unit, principal, state := hostingStateFor(t, kind)
				unit.Capability = capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
				principal.Class = capability.CredentialBearer
				principal.Capability.Audience = capability.Controller
				principal.Capability.Digest = sha256.Sum256([]byte(unit.Capability))
				principal.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "holder"}
				principal.Capability.Grants = []capability.Grant{capability.TriggerHost}
				if kind == capability.UnitNode {
					principal.Capability.Grants = []capability.Grant{capability.NodeReport, capability.DataRead}
				}
				principal.Capability.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "repo", RunID: unit.RunID}
				captured := []string{"PATH=/captured/bin", "RUN_NOTE=requester"}
				snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
				if err != nil {
					t.Fatal(err)
				}
				if local {
					unit.Request.Request.Env, err = snapshots.Capture(t.Context(), captured)
					if err != nil {
						t.Fatal(err)
					}
					unit.Request.Request.EnvironmentSource = runspec.EnvironmentCaptured
					state.snapshot.Run.Request = unit.Request
				}
				prefix := "/api/v1/runs/run/local-host"
				if kind == capability.UnitNode {
					prefix += "/nodes/job"
				}
				var heartbeats []controllerwire.RenewalRequest
				snapshotsRead := 0
				transport := nativeHostingTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/source-bundle" {
						if r.Header.Get("Authorization") != "" {
							t.Fatal("source transfer received an execution bearer")
						}
						response := httptest.NewRecorder()
						response.WriteHeader(http.StatusNoContent)
						return response.Result(), nil
					}
					if got := r.Header.Get("Authorization"); got != "Bearer "+unit.Capability {
						t.Fatalf("host substituted claimant authority: %q", got)
					}
					if r.URL.Host != "sparkwing-api" {
						t.Fatalf("host lost socket destination: %s", r.URL)
					}
					response := httptest.NewRecorder()
					var body any
					switch r.Method + " " + r.URL.Path {
					case "POST " + prefix + "/authentication":
						now := time.Now()
						body = localhostwire.LocalAuthentication{Snapshot: capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: principal}}, Binding: capability.BindingState{Current: principal.Capability.Binding, LeaseExpiresAt: now.Add(time.Hour)}}, Digests: [][32]byte{principal.Capability.Digest}}
					case "GET " + prefix + "/data-authority":
						body = localhostwire.LocalDataAuthority{Authority: blob.Authority{CapabilityRef: "award", Principal: "holder", Repo: "repo"}}
					case "GET " + prefix + "/snapshot":
						snapshotsRead++
						body = state.snapshot
					case "POST /api/v1/claims/unit/log-segments":
						var request blob.LogSigningRequest
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Fatal(err)
						}
						body = blob.LogUploadURL{Subject: request.Subject, Index: request.Index, Committed: true}
					case "POST /api/v1/claims/unit/heartbeat":
						var request controllerwire.RenewalRequest
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Fatal(err)
						}
						heartbeats = append(heartbeats, request)
						body = controllerwire.RenewalResponse{ExpiresAt: time.Now().Add(time.Hour)}
					default:
						t.Fatalf("unexpected host request: %s %s", r.Method, r.URL)
					}
					if err := json.NewEncoder(response).Encode(body); err != nil {
						t.Fatal(err)
					}
					return response.Result(), nil
				})
				connection := client.EngineOptions{URL: "http://sparkwing-api", HTTP: &http.Client{Transport: transport}, Launcher: true, Bearer: func(context.Context) (string, error) { return "claimant", nil }}
				controllerOptions := connection
				controllerOptions.Launcher = false
				controllerOptions.Bearer = func(context.Context) (string, error) { return unit.Capability, nil }
				controller, err := client.NewEngineClient(controllerOptions)
				if err != nil {
					t.Fatal(err)
				}
				config := NativeHostingConfig{
					Home: home, Controller: connection, PrepareSource: true,
					Source: func(ctx context.Context, got controllerwire.ClaimedUnit, options client.EngineOptions) (source.Options, error) {
						bearer, err := options.Bearer(ctx)
						if err != nil || bearer != unit.Capability || options.Launcher || options.HTTP != connection.HTTP || got.RunID != unit.RunID {
							t.Fatal("source options lost award binding")
						}
						return source.Options{Engines: hostingOwnerEngines{}, Workspace: filepath.Join(home, "workspaces"), Base: procenv.Base{Local: local, Ambient: []string{"RUN_NOTE=runner"}}}, nil
					},
				}
				if kind == capability.UnitTrigger {
					unit.Request.Request.Burst = &runspec.BurstPlacement{}
					config.RequesterSecrets = map[string]string{"TOKEN": "captured-secret"}
				}
				services, got, err := NativeHosting(config).Bind(t.Context(), unit, controller)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := services.Close(); err != nil {
						t.Error(err)
					}
				})
				if got.Authority != principal.Authority || services.Config.Source.BundleTransport == nil {
					t.Fatal("native composition lost transport or principal")
				}
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://sparkwing-api/source-bundle", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := services.Config.Source.BundleTransport.RoundTrip(request)
				if err != nil {
					t.Fatal(err)
				}
				if err := response.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusNoContent {
					t.Fatal("source download lost the private controller transport")
				}
				before := snapshotsRead
				snapshot, err := services.Config.Store.ReadSnapshot(t.Context(), unit.RunID)
				if err != nil || snapshot.Run.ID != unit.RunID || snapshotsRead != before+1 {
					t.Fatal("native state did not read through the awarded API")
				}
				if local && !reflect.DeepEqual(services.Config.Source.Base.Captured, captured) {
					t.Fatalf("host replaced the requester's sealed environment: %v", services.Config.Source.Base.Captured)
				}
				prepared := services.Config.Source.Prepared
				if prepared == nil || prepared.Bytes == nil || prepared.Records == nil || prepared.Authority.CapabilityRef != principal.Capability.Prefix || prepared.Publish != (kind != capability.UnitNode) {
					t.Fatal("prepared source lost its award or publication role")
				}
				if kind == capability.UnitTrigger {
					if len(heartbeats) != 1 || !reflect.DeepEqual(heartbeats[0].LocalSecrets, config.RequesterSecrets) {
						t.Fatal("requester secrets did not reach award renewal")
					}
					config.RequesterSecrets["TOKEN"] = "runner-secret"
					actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, Owner: *state.snapshot.Run.Owner}
					if _, err := services.Config.Owner.Renew(t.Context(), actor, false); err != nil {
						t.Fatal(err)
					}
					if len(heartbeats) != 2 || heartbeats[1].LocalSecrets["TOKEN"] != "captured-secret" {
						t.Fatal("host retained mutable requester input")
					}
				}
				if _, _, _, err := nodehost.NewNativeHost(services.Config); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
