package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type burstRunDependencies struct {
	connect func(context.Context, string) (client.EngineOptions, func(), error)
	probe   func(context.Context, client.EngineOptions) error
	local   func(context.Context, string, runFlags, []string) error
	cloud   func(context.Context, string, runFlags, []string, userconfig.RunRouting, client.EngineOptions) error
}

type burstBeforeIntakeError struct{ error }

func (e *burstBeforeIntakeError) Unwrap() error { return e.error }

func burstControllerUnreachable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var network net.Error
	var failure *apierr.Error
	return errors.As(err, &network) || errors.As(err, &failure) && failure.Code == apierr.Unavailable
}

func resolveRunRouting(machine userconfig.Config, project *projectconfig.Config, pipeline string) userconfig.RunRouting {
	var route userconfig.RunRouting
	if machine.Run != nil && machine.Run.Routing != nil {
		route = *machine.Run.Routing
	}
	restrict := func(r *projectconfig.RunRestrictions) {
		if r != nil && r.Route != nil {
			route.Route = new("local")
		}
	}
	if project != nil {
		if project.Defaults != nil {
			restrict(project.Defaults.Run)
		}
		for _, p := range project.Pipelines {
			if p.Name == pipeline {
				restrict(p.Run)
				break
			}
		}
	}
	return route
}

func runRoutedCommand(ctx context.Context, pipeline string, flags runFlags, args []string, route userconfig.RunRouting, out io.Writer, deps burstRunDependencies) error {
	if route.Route == nil || *route.Route == "local" {
		return deps.local(ctx, pipeline, flags, args)
	}
	if err := validateLocalRunFlags(flags); err != nil {
		return err
	}
	if flags.noBincache || flags.hashAllFiles || strings.TrimSpace(flags.priority) == "front" || strings.TrimSpace(flags.priority) == "back" {
		return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "run: Cloud overflow requires retained binary policy and relative-priority bindings before intake"}
	}
	if flags.ref != "" || flags.pipelineRef != "" {
		return errors.New("run: Cloud overflow captures the working tree; use pipeline trigger to select committed revisions")
	}
	if route.Profile == nil {
		return errors.New("run: cloud routing requires a profile in machine run.routing")
	}
	if flags.detached {
		return errors.New("run: a cloud-routed run hosts here and must remain attached")
	}
	options, closeConnection, err := deps.connect(ctx, *route.Profile)
	if err == nil {
		defer closeConnection()
		err = deps.probe(ctx, options)
	}
	if err == nil {
		err = deps.cloud(ctx, pipeline, flags, args, route, options)
		var beforeIntake *burstBeforeIntakeError
		if !errors.As(err, &beforeIntake) {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !burstControllerUnreachable(err) {
		return err
	}
	if _, err := fmt.Fprintln(out, "Cloud controller is unreachable; running locally."); err != nil {
		return err
	}
	return deps.local(ctx, pipeline, flags, args)
}

func runConfiguredInvocation(pipeline string, flags runFlags, args []string) error {
	directory, err := findSparkwingDir()
	if err != nil {
		return err
	}
	route, err := loadRunRouting(directory, pipeline)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return runRoutedCommand(ctx, pipeline, flags, args, route, os.Stderr, burstRunDependencies{
		connect: controllerOptions, probe: client.CheckIdentity,
		local: func(ctx context.Context, pipeline string, flags runFlags, args []string) error {
			return runLocalInvocation(pipeline, flags, args)
		},
		cloud: runNativeBurst,
	})
}

func loadRunRouting(directory, pipeline string) (userconfig.RunRouting, error) {
	path, err := userconfig.Path()
	if err != nil {
		return userconfig.RunRouting{}, err
	}
	machine, err := userconfig.ReadConfig(path)
	if err != nil && !os.IsNotExist(err) {
		return userconfig.RunRouting{}, err
	}
	project, err := projectconfig.Load(filepath.Join(directory, projectconfig.Filename))
	if err != nil {
		return userconfig.RunRouting{}, err
	}
	return resolveRunRouting(machine, project, pipeline), nil
}
