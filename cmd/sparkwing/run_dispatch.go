package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type runFlags struct {
	sourceSelection         *localSourceSelection
	invocation              clisurface.Invocation
	parseErr                error
	ref                     string
	publish, cacheFrom      string
	pauseBefore, pauseAfter []string
	pauseOnFailure          *bool

	noUpdate bool
	verbose  bool

	noBincache      bool
	hashAllFiles    bool
	allowUnadmitted bool

	workers int

	priority    string
	prioritySet bool

	startAt string
	stopAt  string

	only string

	noCache bool

	dryRun bool

	allow []string

	index string

	runHandleFile string

	detached       bool
	pipelineRef    string
	requestID      string
	idempotencyKey string
	outputFormat   string
	outputOrigin   settings.Origin
}

func validatePriorityFlag(v string) (string, error) {
	s := strings.TrimSpace(v)
	switch s {
	case wingwire.PriorityFront, wingwire.PriorityBack:
		return s, nil
	}
	if _, err := strconv.Atoi(s); err == nil {
		return s, nil
	}
	return "", fmt.Errorf("--sw-priority %q: expected an integer, %q, or %q",
		v, wingwire.PriorityFront, wingwire.PriorityBack)
}

func runFlagsFromInvocation(inv clisurface.Invocation) (runFlags, []string) {
	output, outputOrigin := runOutput(inv, os.Getenv("SPARKWING_LOG_FORMAT"), color.IsInteractiveStdout())
	if output != "pretty" && output != "json" && output != "plain" {
		return runFlags{parseErr: errors.New("SPARKWING_LOG_FORMAT must be pretty, json or plain")}, nil
	}
	workers, _ := strconv.Atoi(inv.Value("sw-workers"))
	var pause *bool
	if inv.Has("sw-pause-on-failure") {
		value := inv.Bool("sw-pause-on-failure")
		pause = &value
	}
	return runFlags{
		invocation: inv,
		publish:    inv.Value("publish"), cacheFrom: inv.Value("sw-cache-from"),
		pauseBefore: inv.Flags["sw-pause-before"], pauseAfter: inv.Flags["sw-pause-after"], pauseOnFailure: pause,
		ref: inv.Value("sw-ref"), pipelineRef: inv.Value("sw-pipeline-ref"),
		noUpdate: inv.Bool("sw-no-update"), verbose: inv.Bool("sw-verbose"),
		noBincache: inv.Bool("sw-no-bincache"), hashAllFiles: inv.Bool("sw-hash-all-files"),
		allowUnadmitted: inv.Bool("sw-allow-unadmitted"), workers: workers,
		priority: inv.Value("sw-priority"), prioritySet: inv.Has("sw-priority"),
		startAt: inv.Value("sw-start-at"), stopAt: inv.Value("sw-stop-at"),
		only: inv.Value("sw-only"), noCache: inv.Bool("sw-no-memo"),
		dryRun: inv.Bool("sw-dry-run"), allow: inv.Flags["sw-allow-risk"],
		index: inv.Value("sw-index"), runHandleFile: inv.Value("sw-run-handle-file"),
		detached: inv.Bool("sw-detached"), idempotencyKey: inv.Value("sw-idempotency-key"), requestID: inv.Value("sw-request-id"),
		outputFormat: output, outputOrigin: outputOrigin,
	}, inv.Args
}

func runOutput(inv clisurface.Invocation, environment string, terminal bool) (string, settings.Origin) {
	if inv.Has("sw-output") {
		return inv.Value("sw-output"), settings.Origin{Source: settings.Flag, Key: "sw-output"}
	}
	if inv.Has("output") {
		return inv.Value("output"), settings.Origin{Source: settings.Flag, Key: "output"}
	}
	if environment != "" {
		return environment, settings.Origin{Source: settings.Env, Key: "SPARKWING_LOG_FORMAT"}
	}
	return inv.Output(terminal), settings.Origin{Source: settings.Default, Key: "output"}
}
