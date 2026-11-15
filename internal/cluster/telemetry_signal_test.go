//go:build !windows

package cluster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestTraceFlushPreservesReturnedFailure(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_TRACE_SIGNAL_HELPER") == "1" {
		endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
		url, credentials := "https://controller.example", runnerCredentials(t)
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &url, CredentialsDir: &credentials}}, nil
		}, Factory: func(ctx context.Context, _ RunnerOptions) (runners.RuntimeConfig, error) {
			go func() {
				<-ctx.Done()
				delivery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				request, err := http.NewRequestWithContext(delivery, http.MethodGet, strings.TrimSuffix(endpoint, "/v1/traces")+"/observed", nil)
				if err == nil {
					response, err := http.DefaultClient.Do(request)
					if err == nil {
						_ = response.Body.Close()
					}
				}
			}()
			return runners.RuntimeConfig{}, context.Canceled
		}}
		os.Args = []string{"sparkwing-runner", "warm", "--controller=" + url, "--credentials-dir=" + credentials}
		mainWithRuntime("fixture", clusterRuntime{Runner: cli})
		return
	}
	observed := make(chan struct{})
	process := make(chan *os.Process, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/observed" {
			close(observed)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		child := <-process
		if err := child.Signal(syscall.SIGTERM); err != nil {
			t.Error(err)
			return
		}
		select {
		case <-observed:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_TEST_TRACE_SIGNAL_HELPER", "1")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTraceFlushPreservesReturnedFailure$")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process <- command.Process
	err = command.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("trace flush reclassified returned failure: %v; %s", err, &output)
	}
}
