package main

import (
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
	environment             *localRunEnvironment
	sourceSelection         *localSourceSelection
	invocation              clisurface.Invocation
	parseErr                error
	ref                     string
	route, profile          string
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
	agent          string
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
	return "", fmt.Errorf("--priority %q: expected an integer, %q, or %q",
		v, wingwire.PriorityFront, wingwire.PriorityBack)
}

func runFlagsFromInvocation(inv clisurface.Invocation) (runFlags, []string) {
	output, outputOrigin := runOutput(inv, os.Getenv("SPARKWING_LOG_FORMAT"), color.IsInteractiveStdout())
	if output != "pretty" && output != "json" && output != "plain" {
		return runFlags{parseErr: fmt.Errorf("SPARKWING_LOG_FORMAT=%q must be pretty, json or plain", output)}, nil
	}
	agent, err := resolveRunAgent(inv.Value("agent"), inv.Has("agent"), os.Getenv)
	if err != nil {
		return runFlags{parseErr: err}, nil
	}
	workers := 0
	if inv.Has("workers") {
		n, err := strconv.Atoi(inv.Value("workers"))
		if err != nil {
			return runFlags{parseErr: err}, nil
		}
		workers = n
	}
	var pause *bool
	if inv.Has("pause-on-failure") {
		value := inv.Bool("pause-on-failure")
		pause = &value
	}
	return runFlags{
		invocation: inv,
		route:      inv.Value("route"), profile: inv.Value("profile"),
		publish: inv.Value("publish"), cacheFrom: inv.Value("cache-from"),
		pauseBefore: inv.Flags["pause-before"], pauseAfter: inv.Flags["pause-after"], pauseOnFailure: pause,
		ref: inv.Value("ref"), pipelineRef: inv.Value("pipeline-ref"),
		noUpdate: inv.Bool("no-update"), verbose: inv.Bool("verbose"),
		noBincache: inv.Bool("no-bincache"), hashAllFiles: inv.Bool("hash-all-files"),
		allowUnadmitted: inv.Bool("allow-unadmitted"), workers: workers,
		priority: inv.Value("priority"), prioritySet: inv.Has("priority"),
		startAt: inv.Value("start-at"), stopAt: inv.Value("stop-at"),
		only: inv.Value("only"), noCache: inv.Bool("no-memo"),
		dryRun: inv.Bool("dry-run"), allow: inv.Flags["allow-risk"],
		index: inv.Value("index"), runHandleFile: inv.Value("run-handle-file"),
		detached: inv.Bool("detach"), idempotencyKey: inv.Value("idempotency-key"), requestID: inv.Value("request-id"),
		agent:        agent,
		outputFormat: output, outputOrigin: outputOrigin,
	}, inv.Args
}

// safety: run's execution, help and errors share this one resolution, so a
// failure never renders in a different mode from the run it reports.
func invocationOutput(inv clisurface.Invocation) string {
	terminal := color.IsInteractiveStdout()
	if inv.Command.Path != "sparkwing run" {
		return inv.Output(terminal)
	}
	output, _ := runOutput(inv, os.Getenv("SPARKWING_LOG_FORMAT"), terminal)
	return output
}

func runOutput(inv clisurface.Invocation, environment string, terminal bool) (string, settings.Origin) {
	if inv.Has("output") {
		return inv.Value("output"), settings.Origin{Source: settings.Flag, Key: "output"}
	}
	if environment != "" {
		return environment, settings.Origin{Source: settings.Env, Key: "SPARKWING_LOG_FORMAT"}
	}
	return inv.Output(terminal), settings.Origin{Source: settings.Default, Key: "output"}
}
