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
					if scenario.ID == "protocol-go-sdk-fake-host-and-linkage" && target.ID == "local-go-"+runtime.GOOS {
						t.Run("sdk-linkage", func(t *testing.T) {
							linked, err := acceptance.GoSDKLinkage(t.Context())
							if err != nil {
								t.Fatalf("%v; linked=%v", err, linked)
							}
						})
						t.Run("public-fake-host", testGoSDKFakeHost)
						t.Run("real-process-plan-and-output", testGoSDKProcessPlanOutput)
						t.Run("real-process-host-routes", testGoSDKProcessRoutes)
						t.Skip("failure-result decoding still depends on the unfinished C2 error decoder")
					}
					t.Skipf("contract skeleton: %s on %s requires its placement driver", scenario.ID, target.ID)
				})
			}
		})
	}
}

func testGoSDKProcessPlanOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fd3/fd4 process fixture applies to Unix; Windows handle execution has a separate cell")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	actual, err := acceptance.RunGoSDKProcessFixture(ctx, "", "producer")
	if err != nil {
		t.Fatal(err)
	}
	compareGoSDKResults(t, ctx, actual, []string{"producer"})
}

func testGoSDKProcessRoutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fd3/fd4 process fixture applies to Unix; Windows handle execution has a separate cell")
	}
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
		Runner: sparkwing.RunnerInfo{Name: "local", Type: "local", Labels: []string{"os=linux"}},
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
