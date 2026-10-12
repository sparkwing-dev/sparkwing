package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type githubAppFlags struct {
	AppID         string
	Slug          string
	ClientID      string
	ClientSecret  string
	PrivateKey    string
	WebhookSecret string
}

func configureGitHubApp(srv *controller.Server, f githubAppFlags) error {
	keyText := f.PrivateKey
	if f.AppID == "" && f.Slug == "" && keyText == "" && f.WebhookSecret == "" {
		return nil
	}
	cfg := githubapp.Config{
		Slug: strings.TrimSpace(f.Slug), ClientID: f.ClientID, ClientSecret: f.ClientSecret,
		WebhookSecret: f.WebhookSecret,
	}
	if f.AppID != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(f.AppID), 10, 64)
		if err != nil {
			return fmt.Errorf("--github-app-id: %q is not a number", f.AppID)
		}
		cfg.AppID = id
	}
	if keyText != "" {
		key, err := githubapp.ParsePrivateKey([]byte(keyText))
		if err != nil {
			return fmt.Errorf("GitHub App private key: %w", err)
		}
		cfg.PrivateKey = key
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("GitHub App is half configured: %w; set --github-app-id, --github-app-slug and "+
			"--github-client-id, and add the %s, %s and %s credentials", err,
			credGitHubAppKey, credGitHubAppWebhookSecret, credGitHubClientSecret)
	}
	srv.WithGitHubApp(cfg)
	return nil
}
