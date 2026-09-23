package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type githubAppFlags struct {
	AppID        string
	Slug         string
	ClientID     string
	ClientSecret string
}

// safety: the private key and webhook secret are read from the environment or
// a file only, never a flag, so neither shows up in a process listing.
func configureGitHubApp(srv *controller.Server, f githubAppFlags, getenv func(string) string) error {
	keyFile := getenv("SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE")
	keyText := getenv("SPARKWING_GITHUB_APP_PRIVATE_KEY")
	webhookSecret := getenv("SPARKWING_GITHUB_APP_WEBHOOK_SECRET")
	if f.AppID == "" && f.Slug == "" && keyFile == "" && keyText == "" && webhookSecret == "" {
		return nil
	}
	if keyFile != "" && keyText != "" {
		return errors.New("GitHub App: set SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE or SPARKWING_GITHUB_APP_PRIVATE_KEY, not both")
	}
	if keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return fmt.Errorf("GitHub App private key: %w", err)
		}
		keyText = string(data)
	}
	cfg := githubapp.Config{
		Slug: strings.TrimSpace(f.Slug), ClientID: f.ClientID, ClientSecret: f.ClientSecret,
		WebhookSecret: webhookSecret,
	}
	if f.AppID != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(f.AppID), 10, 64)
		if err != nil {
			return fmt.Errorf("--github-app-id (SPARKWING_GITHUB_APP_ID): %q is not a number", f.AppID)
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
		return fmt.Errorf("GitHub App is half configured: %w; set --github-app-id, --github-app-slug, "+
			"SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE (or _PRIVATE_KEY), SPARKWING_GITHUB_APP_WEBHOOK_SECRET, "+
			"--github-client-id and SPARKWING_GITHUB_CLIENT_SECRET", err)
	}
	srv.WithGitHubApp(cfg)
	return nil
}
