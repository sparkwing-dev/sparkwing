package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestProductionRunnerDoesNotAdvertiseSaturatedNativeJobCapacity(t *testing.T) {
	for _, resource := range []struct {
		name          string
		full, partial wingwire.HostResources
	}{
		{"cores", wingwire.HostResources{Cores: 1}, wingwire.HostResources{Cores: 0.5}},
		{"memory", wingwire.HostResources{MemoryBytes: 128 << 20}, wingwire.HostResources{MemoryBytes: 64 << 20}},
	} {
		t.Run(resource.name, func(t *testing.T) {
			home, err := os.MkdirTemp("", "claim-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			t.Setenv("SPARKWING_HOME", home)
			budget, err := userconfig.ParseBudget("1,128MiB,ignore-external")
			if err != nil {
				t.Fatal(err)
			}
			daemon, err := wingd.New(wingd.Config{Home: home, Version: "v1.2.3", Budget: budget})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- daemon.Run(ctx) }()
			select {
			case <-daemon.Ready():
			case err := <-done:
				cancel()
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			connection, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "v1.2.3"})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			held, err := connection.Acquire(ctx, wingwire.AdmissionRequest{RunID: "busy-local", Resources: resource.full}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if held != nil {
					_ = held.Release()
				}
			}()
			for _, saturated := range []bool{true, false} {
				if !saturated {
					if err := held.Release(); err != nil {
						t.Fatal(err)
					}
					connection, err = wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "v1.2.3"})
					if err != nil {
						t.Fatal(err)
					}
					held, err = connection.Acquire(ctx, wingwire.AdmissionRequest{RunID: "fractional-local", Resources: resource.partial}, nil)
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, kinds := range [][]capability.UnitKind{{capability.UnitNode}, {capability.UnitTrigger, capability.UnitPlan, capability.UnitNode}} {
					for _, enabled := range []bool{false, true} {
						config, err := productionRuntime("v1.2.3").Runner.Factory(ctx, RunnerOptions{Config: userconfig.RunnerConfig{Controller: new("http://controller.invalid"), LocalAdmission: &enabled}})
						if err != nil {
							t.Fatal(err)
						}
						claims := 0
						loopCtx, stop := context.WithCancel(ctx)
						transport := client.EngineOptions{URL: "http://controller.invalid", Bearer: fixedBearer("runner"), HTTP: &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
							var request controllerwire.ClaimRequest
							if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
								return nil, err
							}
							claims++
							hasNodes := slices.Contains(request.Kinds, capability.UnitNode)
							if enabled && saturated && hasNodes && (request.FreeSlots == nil || *request.FreeSlots != 0) {
								t.Errorf("saturated machine offered job capacity: %+v", request)
							}
							if (!enabled || !saturated) && (!hasNodes || request.FreeSlots == nil || *request.FreeSlots != 1) {
								t.Errorf("disabled admission changed claims: %+v", request)
							}
							if len(kinds) > 1 && (!slices.Contains(request.Kinds, capability.UnitTrigger) || !slices.Contains(request.Kinds, capability.UnitPlan) || request.FreeSlots == nil || *request.FreeSlots != 1) {
								t.Errorf("saturation blocked planning owners: %+v", request)
							}
							if claims == 3 {
								stop()
							}
							return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
						})}}
						open := config.NewController
						if open == nil {
							open = client.NewEngineClient
						}
						controller, err := open(transport)
						if err != nil {
							stop()
							t.Fatal(err)
						}
						loop := runners.Loop{Controller: controller, Config: runners.LoopConfig{Version: "v1.2.3", Kinds: kinds, MaxConcurrent: 1, PollInterval: time.Nanosecond}, Host: capacityUnusedHost{}, CheckVersion: func(context.Context, string) error { return nil }}
						err = loop.Run(loopCtx)
						stop()
						if err != nil && !errors.Is(err, context.Canceled) {
							t.Fatal(err)
						}
						if claims != 3 {
							t.Fatalf("claims=%d", claims)
						}
					}
				}
			}
		})
	}
}

type capacityUnusedHost struct{}

func (capacityUnusedHost) Host(context.Context, controllerwire.ClaimedUnit) error {
	panic("empty claim was hosted")
}

func TestNativeClaimCapacityLeavesAdmissionAuthoritativeWithoutDaemon(t *testing.T) {
	home, err := os.MkdirTemp("", "absent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if _, err := wingdclient.Query(t.Context(), wingdclient.Options{Home: home, Version: "v1.2.3"}); !errors.Is(err, wingdclient.ErrNoDaemon) {
		t.Fatalf("fixture daemon state: %v", err)
	}
	calls := 0
	options := client.EngineOptions{URL: "http://controller.invalid", Bearer: fixedBearer("runner"), HTTP: &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
		var request controllerwire.ClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		if request.FreeSlots == nil || *request.FreeSlots != 1 || !slices.Equal(request.Kinds, []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}) {
			t.Errorf("missing daemon changed admission: %+v", request)
		}
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	controller, err := nativeCapacityClient(home, "v1.2.3")(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", FreeSlots: new(1), Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}})
	if err != nil || calls != 1 {
		t.Fatalf("claim could not reach authoritative admission without daemon: calls=%d error=%v", calls, err)
	}
}

func TestRunnerWarnsOncePerUnusableLocalControllerCause(t *testing.T) {
	var logged strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	options := client.EngineOptions{URL: "http://controller.invalid", Bearer: fixedBearer("runner"), HTTP: &http.Client{Transport: productionTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	controller, err := nativeCapacityClient(t.TempDir(), "v1.2.3")(options)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := controller.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", FreeSlots: new(1), Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(logged.String(), "level=WARN"); got != 1 || !strings.Contains(logged.String(), "sparkwing dashboard open") {
		t.Fatalf("runner logged %d warnings for one missing controller:\n%s", got, logged.String())
	}
}
