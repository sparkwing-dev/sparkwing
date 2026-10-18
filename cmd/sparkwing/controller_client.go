package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func resolveEngineController(ctx context.Context, profileName string) (controllerwire.Controller, func(), error) {
	options, closeTransport, err := controllerOptions(ctx, profileName)
	if err != nil {
		return nil, nil, err
	}
	c, err := client.NewEngineClient(options)
	if err != nil {
		closeTransport()
		return nil, nil, err
	}
	return c, closeTransport, nil
}

func resolveRunReader(ctx context.Context, profileName string) (controllerwire.RunReader, func(), error) {
	options, closeTransport, err := controllerOptions(ctx, profileName)
	if err != nil {
		return nil, nil, err
	}
	c, err := client.NewRunReader(options)
	if err != nil {
		closeTransport()
		return nil, nil, err
	}
	return c, closeTransport, nil
}

func controllerOptions(ctx context.Context, profileName string) (client.EngineOptions, func(), error) {
	options := client.EngineOptions{}
	closeTransport := func() {}
	if profileName != "" {
		profile, err := resolveProfileFlag(profileName)
		if err != nil {
			return options, nil, err
		}
		options.URL = profile.ControllerURL()
		options.Bearer = func(context.Context) (string, error) { return profile.ControllerToken(), nil }
	} else {
		home, err := wingd.HomeDir("")
		if err != nil {
			return options, nil, err
		}
		return localControllerOptions(ctx, home, wingdclient.QueryLocalController, resolveLocalServices)
	}
	return options, closeTransport, nil
}

type controllerSocketTransport struct {
	*http.Transport
	socket string
}

func controllerSocketOptions(socket string) (client.EngineOptions, func(), error) {
	if !filepath.IsAbs(socket) {
		return client.EngineOptions{}, nil, &apierr.Error{Code: apierr.Unavailable, Message: "local controller returned a non-absolute API socket"}
	}
	socket = filepath.Clean(socket)
	hc := wingdclient.NewAPIClient(socket)
	transport := &controllerSocketTransport{socket: socket, Transport: hc.Transport.(*http.Transport)}
	hc.Transport = transport
	return client.EngineOptions{URL: wingdclient.APIBaseURL, HTTP: hc}, transport.CloseIdleConnections, nil
}

func localControllerOptions(ctx context.Context, home string, query func(context.Context, string) (wingd.LocalStatus, error), resolve func(context.Context) (dashboardServices, error)) (client.EngineOptions, func(), error) {
	if err := ctx.Err(); err != nil {
		return client.EngineOptions{}, nil, err
	}
	if !filepath.IsAbs(home) {
		return client.EngineOptions{}, nil, &apierr.Error{Code: apierr.Unavailable, Message: "local controller home must be absolute"}
	}
	status, err := query(ctx, home)
	if err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
		return client.EngineOptions{}, nil, err
	}
	if err != nil || !status.Running {
		status, err = ensureLocalController(ctx, home, resolve)
		if err != nil {
			return client.EngineOptions{}, nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return client.EngineOptions{}, nil, err
	}
	if !status.Running || status.PID <= 0 || status.Version == "" {
		return client.EngineOptions{}, nil, &apierr.Error{Code: apierr.Unavailable, Message: "local controller has not established API readiness"}
	}
	admission, err := wingd.EnsureSocketPath(home)
	socket := wingd.APISocketBeside(admission)
	if err != nil {
		return client.EngineOptions{}, nil, err
	}
	return controllerSocketOptions(socket)
}
