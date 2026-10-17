package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
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

func setupRefWorktree(sparkwingDir, ref string) (worktreeDir, pipelineDirectory string, cleanup func(), err error) {
	root := filepath.Dir(sparkwingDir)
	options := source.Options{Base: procenv.Base{Local: true, Captured: os.Environ()}}
	spec := source.Spec{RepoKey: "inspection", LocalCheckout: root, PipelineDir: ".sparkwing", CommittedCheckout: true}
	git, _, err := source.ResolveRevisions(context.Background(), runspec.Git{Ref: ref}, runspec.Source{}, "", func(ref string) (string, error) { return source.ResolveRef(context.Background(), spec, ref, options) })
	if err != nil {
		return "", "", nil, err
	}
	workspace, err := os.MkdirTemp("", "sparkwing-from-*")
	if err != nil {
		return "", "", nil, err
	}
	options.Workspace = workspace
	spec.SHA = git.SHA
	entry, err := source.PrepareCheckout(context.Background(), spec, options)
	if err != nil {
		return "", "", nil, errors.Join(err, os.RemoveAll(workspace))
	}
	return entry.CheckoutDirectory(), entry.Directory, func() {
		if wd, e := os.Getwd(); e == nil {
			if rel, e := filepath.Rel(workspace, wd); e == nil && filepath.IsLocal(rel) {
				if e := os.Chdir(root); e != nil {
					slog.Warn("could not leave temporary checkout", "error", e)
				}
			}
		}
		if e := errors.Join(entry.Close(), os.RemoveAll(workspace)); e != nil {
			slog.Warn("could not remove temporary checkout", "error", e)
		}
	}, nil
}
