package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
)

type githubAppFlags struct {
	AppID           string
	Slug            string
	ClientID        string
	ClientSecret    string
	PrivateKey      string
	RedirectURIs    string
	CredentialError error
}

func configureGitHubApp(srv *controller.Server, f githubAppFlags, hook deployment.KeyPair) {
	requested := f.AppID != "" || f.Slug != "" || f.PrivateKey != "" || hook.Current() != nil || hook.Previous() != nil
	var cfg githubapp.Config
	var appErr error
	if requested {
		cfg, appErr = githubAppConfig(f, hook)
	}
	signIn, oauthErr := providerConfigured("GitHub", "github", f.ClientID, f.ClientSecret)
	var uris []string
	if signIn && oauthErr == nil {
		uris, oauthErr = redirectAllowlist(f.RedirectURIs)
	}
	if err := errors.Join(f.CredentialError, appErr, oauthErr); err != nil {
		srv.WithGitHubAppUnavailable(err)
		slog.Warn("GitHub App unavailable", "reason", err)
		return
	}
	if requested {
		srv.WithGitHubApp(cfg)
	}
	if signIn {
		srv.WithGitHubSignIn(githubauth.New(githubauth.GitHub(f.ClientID, f.ClientSecret)), uris)
	}
}

func githubAppConfig(f githubAppFlags, hook deployment.KeyPair) (githubapp.Config, error) {
	keyText := f.PrivateKey
	cfg := githubapp.Config{
		Slug: strings.TrimSpace(f.Slug), ClientID: f.ClientID, ClientSecret: f.ClientSecret,
		WebhookSecret: string(hook.Current()), PreviousWebhookSecret: string(hook.Previous()),
	}
	if f.AppID != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(f.AppID), 10, 64)
		if err != nil {
			return githubapp.Config{}, fmt.Errorf("--github-app-id: %q is not a number", f.AppID)
		}
		cfg.AppID = id
	}
	if keyText != "" {
		key, err := githubapp.ParsePrivateKey([]byte(keyText))
		if err != nil {
			return githubapp.Config{}, fmt.Errorf("GitHub App private key: %w", err)
		}
		cfg.PrivateKey = key
	}
	if err := cfg.Validate(); err != nil {
		return githubapp.Config{}, fmt.Errorf("GitHub App is half configured: %w; set --github-app-id, --github-app-slug and "+
			"--github-client-id, and add the %s, %s and %s credentials", err,
			credGitHubAppKey, credGitHubAppWebhookSecret, credGitHubClientSecret)
	}
	return cfg, nil
}
