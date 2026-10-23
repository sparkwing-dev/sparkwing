package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
)

type githubAppFlags struct {
	AppID        string
	Slug         string
	ClientID     string
	ClientSecret string
	PrivateKey   string
}

func configureGitHubApp(srv *controller.Server, f githubAppFlags, hook deployment.KeyPair) {
	if f.AppID == "" && f.Slug == "" && f.PrivateKey == "" && hook.Current() == nil && hook.Previous() == nil {
		return
	}
	cfg, err := githubAppConfig(f, hook)
	if err != nil {
		srv.WithGitHubAppUnavailable(err)
		slog.Warn("GitHub App unavailable", "reason", err)
		return
	}
	srv.WithGitHubApp(cfg)
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
