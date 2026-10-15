package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func TestProtocol(t *testing.T) {
	for _, scenario := range acceptance.Scenarios() {
		if scenario.Group != "protocol" {
			continue
		}
		t.Run(scenario.ID, func(t *testing.T) {
			for _, target := range acceptance.Targets() {
				if !acceptance.Applies(scenario, target) {
					continue
				}
				t.Run(target.ID, func(t *testing.T) {
					if scenario.ID == "protocol-version-and-hard-cut" && target.ID == "local-go-"+runtime.GOOS {
						testGoSDKVersionAndHardCut(t)
						return
					}
					if scenario.ID == "protocol-hello-and-handles" && target.ID == "local-go-"+runtime.GOOS {
						testGoSDKHelloAndHandles(t)
						return
					}
					if scenario.ID == "protocol-mask-acknowledgement" && target.ID == "local-go-"+runtime.GOOS {
						t.Run("sdk-service-contract", testGoSDKMaskAcknowledgement)
						t.Skip("real-host registration requires the missing C2 acknowledged Mask route contract")
					}
					if scenario.ID == "protocol-go-sdk-fake-host-and-linkage" && target.ID == "local-go-"+runtime.GOOS {
						t.Run("sdk-linkage", func(t *testing.T) {
							linked, err := acceptance.GoSDKLinkage(t.Context())
							if err != nil {
								t.Fatalf("%v; linked=%v", err, linked)
							}
						})
						t.Run("public-fake-host", testGoSDKFakeHost)
						t.Run("fake-host-run-snapshot", testGoSDKFakeHostRunSnapshot)
						t.Run("entrypoint-alias", testGoSDKEntrypointAlias)
						t.Run("output-failures", testGoSDKOutputFailures)
						t.Run("interface-outputs", testGoSDKInterfaceOutputs)
						t.Run("real-process-plan-and-output", testGoSDKProcessPlanOutput)
						t.Run("real-process-body-failure", testGoSDKProcessFailure)
						t.Run("real-process-application-retry", testGoSDKRetryStepOutputs)
						t.Run("real-process-host-routes", testGoSDKProcessRoutes)
						return
					}
					t.Skipf("contract skeleton: %s on %s requires its placement driver", scenario.ID, target.ID)
				})
			}
		})
	}
}

func testGoSDKProcessFailure(t *testing.T) {
	actual, err := acceptance.RunGoSDKProcessFixture(t.Context(), "", "failure")
	if err != nil {
		t.Fatal(err)
	}
	got := actual.Steps["failure"]
	if got.Outcome != "failed" || got.Error == nil || got.Error.Code != apierr.SDKError || got.Error.Message != "fixture failure" {
		t.Fatalf("process body failure = %+v", got)
	}
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	plan := sparkwing.NewPlan()
	if err := (protocolgo.Pipeline{}).Plan(t.Context(), plan, protocolgo.Inputs{Name: "demo"}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	_, err = host.RunStep(t.Context(), plan, "failure", "run")
	var failure *apierr.Error
	if !errors.As(err, &failure) || !reflect.DeepEqual(failure, got.Error) {
		t.Fatalf("fake and process body failures differ: fake=%v process=%+v", err, got.Error)
	}
}

func testGoSDKProcessPlanOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	actual, err := acceptance.RunGoSDKProcessFixture(ctx, "", "producer")
	if err != nil {
		t.Fatal(err)
	}
	compareGoSDKResults(t, ctx, actual, []string{"producer"})
}

func testGoSDKProcessRoutes(t *testing.T) {
	var outputReads, secretReads, plainReads atomic.Int32
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("sandbox disallows loopback listener for host routes: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	routes := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/node/v1/jobs/producer/output" &&
			r.Header.Get("Authorization") == "Bearer cap-consumer":
			outputReads.Add(1)
			_, _ = w.Write([]byte(`{"output":{"value":"demo"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/node/v1/secrets/TOKEN" &&
			r.Header.Get("Authorization") == "Bearer cap-secret":
			secretReads.Add(1)
			_, _ = w.Write([]byte(`{"value":"fixture-secret","masked":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/node/v1/secrets/PLAIN" &&
			r.Header.Get("Authorization") == "Bearer cap-secret":
			plainReads.Add(1)
			_, _ = w.Write([]byte(`{"value":"fixture-plain","masked":false}`))
		default:
			t.Errorf("unexpected SDK host route %s %s with %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{}`))
		}
	})}}
	routes.Start()
	t.Cleanup(routes.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	actual, err := acceptance.RunGoSDKProcessFixture(ctx, routes.URL, "producer", "consumer", "secret")
	if err != nil {
		t.Fatal(err)
	}
	compareGoSDKResults(t, ctx, actual, []string{"producer", "consumer", "secret"})
	if outputReads.Load() != 1 || secretReads.Load() != 1 || plainReads.Load() != 1 {
		t.Fatalf("host routes: output=%d secret=%d plain=%d", outputReads.Load(), secretReads.Load(), plainReads.Load())
	}
}

func compareGoSDKResults(t *testing.T, ctx context.Context, actual acceptance.GoSDKProcessResult, jobs []string) {
	t.Helper()
	protocolgo.Register()
	host := sparkwingtest.New(sparkwingtest.Options{
		Runner: sparkwing.RunnerInfo{Name: "local", Type: "local", Labels: []string{"os=" + runtime.GOOS}},
		Run:    sparkwing.RunContext{RunID: "r", Pipeline: protocolgo.Name},
		Secrets: map[string]sparkwingtest.Secret{
			"TOKEN": {Value: "fixture-secret", Masked: true},
			"PLAIN": {Value: "fixture-plain"},
		},
		Outputs: map[string]json.RawMessage{"producer": json.RawMessage(`{"value":"demo"}`)},
	})
	t.Cleanup(func() { _ = host.Close() })
	fakePlan, err := host.Plan(ctx, protocolgo.Name, map[string]string{"name": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual.Plan, fakePlan) {
		t.Fatalf("process and fake plans differ: process=%+v fake=%+v", actual.Plan, fakePlan)
	}
	plan := sparkwing.NewPlan()
	if err := (protocolgo.Pipeline{}).Plan(ctx, plan, protocolgo.Inputs{Name: "demo"}, sparkwing.RunContext{
		RunID: "r", Pipeline: protocolgo.Name,
	}); err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		fakeOutput, err := host.RunStep(ctx, plan, job, "run")
		if err != nil {
			t.Fatalf("fake %s: %v", job, err)
		}
		real := actual.Steps[job]
		if real.Outcome != "success" {
			t.Fatalf("process %s outcome = %+v", job, real)
		}
		var fakeValue, realValue any
		if err := json.Unmarshal(fakeOutput, &fakeValue); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(real.Output, &realValue); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(realValue, fakeValue) {
			t.Fatalf("%s output differs: process=%s fake=%s", job, real.Output, fakeOutput)
		}
	}
}

func testGoSDKFakeHost(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{
		Secrets: map[string]sparkwingtest.Secret{"TOKEN": {Value: "fake-host-secret", Masked: true}},
	})
	t.Cleanup(func() { _ = host.Close() })
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "probe", func(ctx context.Context) error {
		value, err := sparkwing.Secret(ctx, "TOKEN")
		if err != nil || value != "fake-host-secret" {
			return errors.New("fake host did not provide the declared secret")
		}
		sparkwing.Info(ctx, "value=%s", value)
		return errors.New("fixture failure")
	})
	if _, err := host.RunStep(t.Context(), plan, "probe", "run"); err == nil || !strings.Contains(err.Error(), "fixture failure") {
		t.Fatalf("fake host step failure = %v", err)
	}
	records := host.Records()
	if len(records) == 0 || records[len(records)-1].Msg != "value=***" {
		t.Fatalf("fake host masked records = %+v", records)
	}
}
