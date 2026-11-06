package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestLocalRunnerPublishesAfterHostCleanup(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(home, "config.yaml")
			if err := os.WriteFile(config, []byte("profiles:\n  history:\n    controller:\n      url: https://profile.test\n      token: profile-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SPARKWING_CONFIG", config)
			unit := localTestAward(t)
			unit.Request.Request.Options.Publish.Value = "history"
			unit.Request.Request.Source.Location.LocalCheckout = filepath.Join(home, "source")
			now := time.Now()
			state := runstate.LocalPublication{RunSnapshot: runstate.RunSnapshot{Run: runstate.RunRow{Team: "team", Execution: runstate.Execution{
				ID: unit.RunID, Request: unit.Request, FinishedAt: &now, Spec: &runspec.Spec{}, Plan: json.RawMessage(`{"jobs":[]}`),
			}}}}
			var calls []string
			transport := queueHostTransport(func(r *http.Request) (*http.Response, error) {
				if err := r.Context().Err(); err != nil {
					return nil, err
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("publication request has no deadline")
				}
				calls = append(calls, r.Method+" "+r.URL.Path)
				w := httptest.NewRecorder()
				switch r.URL.Host {
				case "sparkwing-api":
					if r.Header.Get("Authorization") != "" || r.URL.Path != "/api/v1/runs/"+unit.RunID+"/local-host/snapshot" {
						t.Fatal("publication did not use peer authority for its terminal snapshot")
					}
					if err := json.NewEncoder(w).Encode(state); err != nil {
						t.Fatal(err)
					}
				case "profile.test":
					switch r.Method + " " + r.URL.Path {
					case "POST /api/v1/runs":
						if r.Header.Get("Authorization") != "Bearer profile-token" {
							t.Fatal("publication used execution authority for the profile")
						}
						var input controllerwire.CreateRunRequest
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						if input.Request.Mode != runspec.IntakePublish || input.Request.Options.Publish.Value != "" || input.Request.Source.Location.LocalCheckout != "" {
							t.Fatalf("publication did not strip execution inputs: %+v", input.Request)
						}
						if refused {
							w.WriteHeader(http.StatusForbidden)
							_, _ = w.WriteString(`{"error":{"code":"permission_denied","message":"publication refused"}}`)
						} else {
							if err := json.NewEncoder(w).Encode(controllerwire.CreateRunResponse{RunID: "published", Number: 1, Inputs: runspec.DisplayRequest(unit.Request), WriteScope: "manual:account:main"}); err != nil {
								t.Fatal(err)
							}
						}
					case "POST /api/v1/tokens":
						if err := json.NewEncoder(w).Encode(controllerwire.CreatedToken{Capability: "publish-token", Prefix: "publisher", Team: "team", Principal: "account"}); err != nil {
							t.Fatal(err)
						}
					case "POST /api/v1/runs/published/plan", "POST /api/v1/runs/published/finish":
						if r.Header.Get("Authorization") != "Bearer publish-token" {
							t.Fatal("publication widened its data credential")
						}
						var result any = controllerwire.Applied{}
						if strings.HasSuffix(r.URL.Path, "/plan") {
							result = controllerwire.PlanAccepted{}
						}
						if err := json.NewEncoder(w).Encode(result); err != nil {
							t.Fatal(err)
						}
					case "DELETE /api/v1/tokens/publisher":
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Fatalf("unexpected publication: %s %s", r.Method, r.URL)
					}
				default:
					t.Fatal(r.URL)
				}
				return w.Result(), nil
			})
			previous := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			var output bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(logger) })
			failure := errors.New("host cleanup failed")
			services := nodehost.NativeServices{Close: func() error { calls = append(calls, "host-close"); return failure }}
			options := client.EngineOptions{URL: "http://sparkwing-api", HTTP: &http.Client{Transport: transport}, Bearer: func(context.Context) (string, error) { return "claimant", nil }}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := bindLocalProfiles(ctx, home, options, unit, &services); err != nil {
				t.Fatal(err)
			}
			cancel()
			if err := services.Close(); !errors.Is(err, failure) {
				t.Fatal("publication changed the host verdict:", err)
			}
			want := []string{"host-close", "GET /api/v1/runs/" + unit.RunID + "/local-host/snapshot", "POST /api/v1/runs"}
			if !refused {
				want = append(want, "POST /api/v1/tokens", "POST /api/v1/runs/published/plan", "POST /api/v1/runs/published/finish", "DELETE /api/v1/tokens/publisher")
			}
			if !slices.Equal(calls, want) || strings.Contains(output.String(), "publication failed") != refused {
				t.Fatalf("publication sequence=%v logs=%s", calls, &output)
			}
		})
	}
}

func TestLocalRunnerProfilesRequireLocalOptIn(t *testing.T) {
	for _, origin := range []runspec.Origin{runspec.OriginLocal, runspec.OriginTrigger} {
		for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
			t.Run(string(origin)+"/"+string(kind), func(t *testing.T) {
				unit := localTestAward(t)
				unit.Unit, unit.Request.Request.Origin = kind, origin
				if origin == runspec.OriginLocal && kind == capability.UnitNode {
					unit.Request.Request.Options.Publish.Value = "unavailable"
				}
				if origin != runspec.OriginLocal {
					unit.Request.Request.Options.CacheFrom.Value = "unavailable"
					unit.Request.Request.Options.Publish.Value = "unavailable"
				}
				closed := 0
				services := nodehost.NativeServices{Close: func() error { closed++; return nil }}
				if err := bindLocalProfiles(t.Context(), t.TempDir(), client.EngineOptions{}, unit, &services); err != nil {
					t.Fatal(err)
				}
				if err := services.Close(); err != nil || closed != 1 || services.Storage.RemoteCache != nil {
					t.Fatalf("unselected profile changed hosting: closed=%d error=%v", closed, err)
				}
			})
		}
	}
}
