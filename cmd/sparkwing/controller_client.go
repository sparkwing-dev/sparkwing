package main

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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
		d, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Version: installedVersion()})
		if err != nil {
			return options, nil, err
		}
		ready, problem, socket := d.APIReady(), d.APIError(), d.APISocket()
		if err := d.Close(); err != nil {
			return options, nil, err
		}
		if !ready || socket == "" {
			return options, nil, &apierr.Error{Code: apierr.Unavailable, Message: "local controller API is unavailable", Hint: "run sparkwing doctor", Details: map[string]any{"reason": problem}}
		}
		return controllerSocketOptions(socket)
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
	httpClient := wingdclient.NewAPIClient(socket)
	transport := &controllerSocketTransport{socket: socket, Transport: httpClient.Transport.(*http.Transport)}
	httpClient.Transport = transport
	options := client.EngineOptions{URL: wingdclient.APIBaseURL, HTTP: httpClient}
	return options, transport.CloseIdleConnections, nil
}
