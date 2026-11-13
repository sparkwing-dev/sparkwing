package cluster

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestRunnerExportsAndFlushesConfiguredTracing(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_TELEMETRY_HELPER") == "1" {
		old := otel.GetTracerProvider()
		verb := os.Getenv("SPARKWING_TEST_TELEMETRY_COMMAND")
		os.Args = []string{"sparkwing-runner", verb, "--help"}
		mainWithRuntime("test", clusterRuntime{})
		if os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" && otel.GetTracerProvider() != old {
			t.Fatal("unconfigured runner initialized telemetry")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"start", "warm", "run-node"} {
		for _, configured := range []bool{false, true} {
			t.Run(verb+"/"+map[bool]string{false: "unset", true: "configured"}[configured], func(t *testing.T) {
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
					w.WriteHeader(http.StatusOK)
				}))
				defer collector.Close()
				t.Setenv("SPARKWING_TEST_TELEMETRY_HELPER", "1")
				t.Setenv("SPARKWING_TEST_TELEMETRY_COMMAND", verb)
				for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
					t.Setenv(key, "")
				}
				if configured {
					t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
				}
				t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
				command := exec.CommandContext(t.Context(), executable, "-test.run=^TestRunnerExportsAndFlushesConfiguredTracing$")
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("runner: %s %v", output, err)
				}
				mu.Lock()
				data := bytes.Clone(exported.Bytes())
				mu.Unlock()
				if configured && !bytes.Contains(data, []byte("runner "+verb)) {
					t.Fatalf("runner did not flush its span: %q", data)
				}
				if !configured && len(data) != 0 {
					t.Fatal("unconfigured runner exported telemetry")
				}
			})
		}
	}
}
