package main

import (
	"context"
	"errors"
	"strconv"
	"strings"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func lookupTriggerGitHubID(ctx context.Context, remote, profile string, captured []string) (int64, error) {
	options, closeTransport, err := controllerOptions(ctx, profile)
	if err != nil {
		return 0, err
	}
	defer closeTransport()
	repository, err := client.ResolveRepository(ctx, options, remote)
	if err != nil {
		var failure *apierr.Error
		if ctx.Err() != nil || !errors.As(err, &failure) {
			return 0, err
		}
		fallback := failure.Code == apierr.NotFound || failure.Code == hosterr.RateLimited ||
			failure.Code == hosterr.Forbidden && (failure.Details["reason"] == nil || failure.Details["reason"] == string(hosterr.ReasonRepositoryUnauthorized))
		if !fallback {
			return 0, err
		}
		env := make(map[string]string)
		for _, item := range captured {
			key, value, _ := strings.Cut(item, "=")
			env[key] = value
		}
		token := env["GH_TOKEN"]
		if token == "" {
			token = env["GITHUB_TOKEN"]
		}
		if token == "" {
			return 0, err
		}
		id, lookupErr := githubapp.RepositoryID(ctx, remote, token)
		if lookupErr != nil {
			return 0, err
		}
		return id, nil
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(string(repository.Key), "gh:"), 10, 64)
	if err != nil || id <= 0 || !strings.HasPrefix(string(repository.Key), "gh:") {
		return 0, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid GitHub repository identity"}
	}
	return id, nil
}
