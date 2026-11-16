package otelutil

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTracesOnlyExporterFailuresNeverEchoEndpointSecrets(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_HOSTILE_EXPORTER_HELPER") == "1" {
		telemetry, err := Init(context.Background(), Config{ServiceName: "sparkwing-runner", Version: "test", TracesOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, root := StartOperation(context.Background(), "runner run-node")
		_, child := StartOperation(ctx, "controller POST /api/v1/x")
		child.Phase("http.attempt")
		child.Finish(nil)
		root.Finish(nil)
		flush, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = telemetry.Shutdown(flush)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("SECRET-BODY-" + r.Header.Get("Authorization")))
	}))
	defer refusing.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for name, endpoint := range map[string]string{
		"refusing": strings.Replace(refusing.URL, "http://", "http://SECRETUSER:SECRETPASS@", 1) + "/SECRETPATH/v1/traces?token=SECRETQUERY",
		"closed":   "http://SECRETUSER:SECRETPASS@" + closedAddr + "/SECRETPATH/v1/traces?token=SECRETQUERY",
		"invalid":  "http://SECRETUSER:SECRETPASS@[::1/SECRETPATH?token=SECRETQUERY",
	} {
		t.Run(name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestTracesOnlyExporterFailuresNeverEchoEndpointSecrets$")
			command.Env = append(os.Environ(), "SPARKWING_TEST_HOSTILE_EXPORTER_HELPER=1",
				"OTEL_EXPORTER_OTLP_ENDPOINT=", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="+endpoint,
				"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer SECRETBEARER",
				"OTEL_EXPORTER_OTLP_TIMEOUT=500", "OTEL_TRACES_SAMPLER_ARG=1")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("helper: %v\n%s", err, output)
			}
			for _, secret := range []string{"SECRETUSER", "SECRETPASS", "SECRETPATH", "SECRETQUERY", "SECRETBEARER", "SECRET-BODY"} {
				if bytes.Contains(output, []byte(secret)) {
					t.Errorf("exporter failure leaked %s: %s", secret, output)
				}
			}
		})
	}
}
