package main

import (
	"fmt"
	"log/slog"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/originguard"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type identityFlags struct {
	MultiTeam          bool
	GoogleClientID     string
	GoogleClientSecret string
	GitHubApp          githubAppFlags
	GitHubHook         deployment.KeyPair
	RedirectURIs       string
	SignUpGate         string
}

type identityConfig struct {
	flags      identityFlags
	waitlist   bool
	googleURIs []string
}

// safety: fatal sign-in configuration is checked before startup creates persistent
// state. GitHub App settings are checked later and only disable the App.
func parseIdentity(f identityFlags) (identityConfig, error) {
	gate, err := store.ParseSignUpMode(f.SignUpGate)
	if err != nil {
		return identityConfig{}, fmt.Errorf("--signup-gate: %w", err)
	}
	cfg := identityConfig{flags: f, waitlist: gate == store.SignUpWaitlist}
	google, err := providerConfigured("Google", "google", f.GoogleClientID, f.GoogleClientSecret)
	if err != nil {
		return identityConfig{}, err
	}
	github := f.GitHubApp.AppID != "" || f.GitHubApp.Slug != "" || f.GitHubApp.PrivateKey != "" ||
		f.GitHubApp.ClientID != "" || f.GitHubApp.ClientSecret != "" || f.GitHubApp.CredentialError != nil ||
		f.GitHubHook.Current() != nil || f.GitHubHook.Previous() != nil
	if google || (f.RedirectURIs != "" && !github) {
		uris, err := redirectAllowlist(f.RedirectURIs)
		if err != nil {
			return identityConfig{}, err
		}
		if google {
			cfg.googleURIs = uris
		}
	}
	return cfg, nil
}

func configureIdentity(srv *controller.Server, cfg identityConfig, logger *slog.Logger) {
	f := cfg.flags
	srv.WithSignUpWaitlist(cfg.waitlist)
	if len(cfg.googleURIs) != 0 {
		srv.WithGoogleSignIn(googleauth.New(googleauth.Google(f.GoogleClientID, f.GoogleClientSecret)), cfg.googleURIs)
	}
	f.GitHubApp.RedirectURIs = f.RedirectURIs
	configureGitHubApp(srv, f.GitHubApp, f.GitHubHook)
	if len(cfg.googleURIs) == 0 && f.GitHubApp.ClientID == "" && f.GitHubApp.ClientSecret == "" {
		return
	}
	if !f.MultiTeam {
		logger.Warn("sign-in is configured but not offered: it needs --deployment=team or cloud")
	}
}

func providerConfigured(label, flag, id, secret string) (bool, error) {
	if id == "" && secret == "" {
		return false, nil
	}
	if id == "" || secret == "" {
		return false, fmt.Errorf("%s sign-in needs both --%s-client-id and the %s-client-secret credential",
			label, flag, flag)
	}
	return true, nil
}

func redirectAllowlist(raw string) ([]string, error) {
	uris := splitCSV(raw)
	if len(uris) == 0 {
		return nil, fmt.Errorf("sign-in needs --oauth-redirect-uris, " +
			"the dashboard callback URLs a provider may send a browser back to")
	}
	for _, u := range uris {
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("--oauth-redirect-uris: %q is not an absolute http(s) URL without a query", u)
		}
		if parsed.Scheme == "http" && !originguard.LoopbackHost(parsed.Host) {
			return nil, fmt.Errorf("--oauth-redirect-uris: %q uses http on a host that is not loopback", u)
		}
	}
	return uris, nil
}
