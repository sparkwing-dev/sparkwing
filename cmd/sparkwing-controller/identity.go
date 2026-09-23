package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type identityFlags struct {
	LicenseFile        string
	GoogleClientID     string
	GoogleClientSecret string
	GitHubClientID     string
	GitHubClientSecret string
	RedirectURIs       string
	SignUpGate         string
}

func readLicense(path string) (string, error) {
	if path == "" {
		return os.Getenv("SPARKWING_LICENSE"), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--license-file: %w", err)
	}
	return string(data), nil
}

// safety: an unusable license only logs, because a local install runs without one; a half-configured
// Google client is refused, because it would draw a sign-in button that cannot finish.
func configureIdentity(srv *controller.Server, f identityFlags, logger *slog.Logger) error {
	raw, err := readLicense(f.LicenseFile)
	if err != nil {
		return err
	}
	key, err := license.EmbeddedKey()
	if err != nil {
		return err
	}
	srv.WithLicense(license.Resolve(raw, key, time.Now(), logger))
	gate, err := store.ParseSignUpMode(f.SignUpGate)
	if err != nil {
		return fmt.Errorf("--signup-gate: %w", err)
	}
	srv.WithSignUpWaitlist(gate == store.SignUpWaitlist)
	// hack: the storage pass that reports the free tier is being redesigned; once the server
	// implements FreeTierSource itself, this wires it with no further change here.
	if src, ok := any(srv).(controller.FreeTierSource); ok {
		srv.WithFreeTier(src)
	}
	srv.CheckFreeTierSource()

	google, err := providerConfigured("Google", "google", f.GoogleClientID, f.GoogleClientSecret)
	if err != nil {
		return err
	}
	github, err := providerConfigured("GitHub", "github", f.GitHubClientID, f.GitHubClientSecret)
	if err != nil {
		return err
	}
	if !google && !github {
		return nil
	}
	uris, err := redirectAllowlist(f.RedirectURIs)
	if err != nil {
		return err
	}
	if google {
		srv.WithGoogleSignIn(googleauth.New(googleauth.Google(f.GoogleClientID, f.GoogleClientSecret)), uris)
	}
	if github {
		srv.WithGitHubSignIn(githubauth.New(githubauth.GitHub(f.GitHubClientID, f.GitHubClientSecret)), uris)
	}
	if !srv.MultiTeam() {
		logger.Warn("sign-in is configured but not offered: it needs a multi-team license")
	}
	return nil
}

func providerConfigured(label, flag, id, secret string) (bool, error) {
	if id == "" && secret == "" {
		return false, nil
	}
	if id == "" || secret == "" {
		upper := strings.ToUpper(flag)
		return false, fmt.Errorf("%s sign-in needs both --%s-client-id (SPARKWING_%s_CLIENT_ID) and SPARKWING_%s_CLIENT_SECRET",
			label, flag, upper, upper)
	}
	return true, nil
}

func redirectAllowlist(raw string) ([]string, error) {
	uris := splitCSV(raw)
	if len(uris) == 0 {
		return nil, fmt.Errorf("sign-in needs --oauth-redirect-uris (SPARKWING_OAUTH_REDIRECT_URIS), " +
			"the dashboard callback URLs a provider may send a browser back to")
	}
	for _, u := range uris {
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("--oauth-redirect-uris: %q is not an absolute http(s) URL without a query", u)
		}
		if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
			return nil, fmt.Errorf("--oauth-redirect-uris: %q uses http on a host that is not loopback", u)
		}
	}
	return uris, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}
