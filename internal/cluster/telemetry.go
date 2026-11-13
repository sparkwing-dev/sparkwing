package cluster

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

func runnerTelemetry(ctx context.Context, command, version string) (context.Context, func(error), error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return ctx, func(error) {}, nil
	}
	telemetry, err := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-runner", Version: version, TracesOnly: true})
	if err != nil {
		return ctx, nil, err
	}
	ctx, operation := otelutil.StartOperation(ctx, "runner "+command)
	return ctx, func(err error) {
		operation.Finish(err)
		flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if telemetry.Shutdown(flush) != nil {
			fmt.Fprintln(os.Stderr, "warning: runner trace flush failed")
		}
	}, nil
}
