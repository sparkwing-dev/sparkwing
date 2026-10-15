package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func resolveEngineController(ctx context.Context, profileName string) (controllerwire.Controller, func(), error) {
	options := client.EngineOptions{}
	closeTransport := func() {}
	if profileName != "" {
		profile, err := resolveProfileFlag(profileName)
		if err != nil {
			return nil, nil, err
		}
		options.URL = profile.ControllerURL()
		options.Bearer = func(context.Context) (string, error) { return profile.ControllerToken(), nil }
	} else {
		d, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Version: installedVersion()})
		if err != nil {
			return nil, nil, err
		}
		ready, problem, socket := d.APIReady(), d.APIError(), d.APISocket()
		if err := d.Close(); err != nil {
			return nil, nil, err
		}
		if !ready || socket == "" {
			return nil, nil, &apierr.Error{Code: apierr.Unavailable, Message: "local controller API is unavailable", Hint: "run sparkwing doctor", Details: map[string]any{"reason": problem}}
		}
		httpClient := orchestrator.NewAPISocketClient(socket)
		options.URL, options.HTTP = orchestrator.HostedAPIBaseURL, httpClient
		closeTransport = httpClient.CloseIdleConnections
	}
	c, err := client.NewEngineClient(options)
	if err != nil {
		closeTransport()
		return nil, nil, err
	}

	return c, closeTransport, nil
}
