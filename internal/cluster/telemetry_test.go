package cluster

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

func TestRunnerExportsAndFlushesConfiguredTracing(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_TELEMETRY_HELPER") == "1" {
		old := otel.GetTracerProvider()
		os.Args = []string{"sparkwing-runner", os.Getenv("SPARKWING_TEST_TELEMETRY_COMMAND"), os.Getenv("SPARKWING_TEST_TELEMETRY_ARG")}
		mainWithRuntime("test", clusterRuntime{})
		if otel.GetTracerProvider() != old {
			t.Fatal("help initialized telemetry")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"start", "warm", "run-node"} {
		for _, mode := range []string{"unset", "configured", "refused", "help"} {
			configured := mode != "unset"
			wantExport := configured && mode != "help"
			t.Run(verb+"/"+mode, func(t *testing.T) {
				var mu sync.Mutex
				var exported bytes.Buffer
				collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					mu.Lock()
					exported.Write(body)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/x-protobuf")
					if mode == "refused" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte("private-exporter-body-token"))
						return
					}
					w.WriteHeader(http.StatusOK)
				}))
				defer collector.Close()
				t.Setenv("SPARKWING_TEST_TELEMETRY_HELPER", "1")
				t.Setenv("SPARKWING_TEST_TELEMETRY_COMMAND", verb)
				arg, wantExit := "--trace-test-unknown-flag", 2
				if mode == "help" {
					arg, wantExit = "--help", 0
				}
				t.Setenv("SPARKWING_TEST_TELEMETRY_ARG", arg)
				for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
					t.Setenv(key, "")
				}
				if configured {
					t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/private-exporter-path-token")
				}
				t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
				command := exec.CommandContext(t.Context(), executable, "-test.run=^TestRunnerExportsAndFlushesConfiguredTracing$")
				output, err := command.CombinedOutput()
				code := 0
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					code = exit.ExitCode()
				} else if err != nil {
					t.Fatal(err)
				}
				if code != wantExit {
					t.Fatalf("runner exit %d, want %d: %s", code, wantExit, output)
				}
				for _, secret := range []string{"private-exporter-path-token", "private-exporter-body-token"} {
					if bytes.Contains(output, []byte(secret)) {
						t.Errorf("trace-export failure leaked endpoint or body: %s", output)
					}
				}
				mu.Lock()
				data := bytes.Clone(exported.Bytes())
				mu.Unlock()
				if wantExport && !bytes.Contains(data, []byte("runner "+verb)) {
					t.Fatalf("runner did not flush its span: %q", data)
				}
				if !wantExport && len(data) != 0 {
					t.Fatalf("%s runner exported telemetry", mode)
				}
			})
		}
	}
}

func TestLongLivedRunnerRequestsStartTheirOwnTraces(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		t.Setenv(key, "")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
	oldProvider, oldPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTracerProvider(oldProvider); otel.SetTextMapPropagator(oldPropagator) })
	for verb, shared := range map[string]bool{"start": false, "warm": false, "run-node": true} {
		t.Run(verb, func(t *testing.T) {
			ctx, finish, err := runnerTelemetry(t.Context(), verb, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer finish(nil)
			firstCtx, first := otelutil.StartOperation(ctx, "first request")
			secondCtx, second := otelutil.StartOperation(ctx, "second request")
			a, b := trace.SpanContextFromContext(firstCtx), trace.SpanContextFromContext(secondCtx)
			first.Finish(nil)
			second.Finish(nil)
			if !a.IsValid() || !b.IsValid() {
				t.Fatal("configured runner did not sample its requests")
			}
			if (a.TraceID() == b.TraceID()) != shared {
				t.Fatalf("%s requests share trace=%v, want %v", verb, a.TraceID() == b.TraceID(), shared)
			}
		})
	}
}
