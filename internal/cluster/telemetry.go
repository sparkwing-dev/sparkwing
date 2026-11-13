package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
)

func runnerTelemetry(ctx context.Context, command, version string) (context.Context, func(error), error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return ctx, func(error) {}, nil
	}
	telemetry, err := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-runner", Version: version, TracesOnly: true})
	if err != nil {
		return ctx, nil, err
	}
	finishRoot := func(error) {}
	// safety: start and warm run for days; a lifetime root would put every request in one unbounded
	// trace under one sampling decision, so only bounded run-node keeps a root span.
	if command == "run-node" {
		var root *otelutil.Operation
		ctx, root = otelutil.StartOperation(ctx, "runner "+command)
		finishRoot = root.Finish
	} else {
		_, marker := otelutil.StartOperation(ctx, "runner "+command)
		marker.Finish(nil)
	}
	return ctx, func(err error) {
		finishRoot(err)
		flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if telemetry.Shutdown(flush) != nil {
			fmt.Fprintln(os.Stderr, "warning: runner trace flush failed")
		}
	}, nil
}

func requestsHelp(command string, args []string) bool {
	_, err := runnerflags.Parse(command, args, io.Discard, io.Discard)
	return errors.Is(err, flag.ErrHelp)
}
