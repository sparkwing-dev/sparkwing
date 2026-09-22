package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type identityFlags struct {
	LicenseFile        string
	GoogleClientID     string
	GoogleClientSecret string
	RedirectURIs       string
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

	if f.GoogleClientID == "" && f.GoogleClientSecret == "" {
		return nil
	}
	if f.GoogleClientID == "" || f.GoogleClientSecret == "" {
		return fmt.Errorf("google sign-in needs both --google-client-id (SPARKWING_GOOGLE_CLIENT_ID) " +
			"and SPARKWING_GOOGLE_CLIENT_SECRET")
	}
	uris := splitCSV(f.RedirectURIs)
	if len(uris) == 0 {
		return fmt.Errorf("google sign-in needs --oauth-redirect-uris (SPARKWING_OAUTH_REDIRECT_URIS), " +
			"the dashboard callback URLs Google may send a browser back to")
	}
	for _, u := range uris {
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("--oauth-redirect-uris: %q is not an absolute http(s) URL without a query", u)
		}
		if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
			return fmt.Errorf("--oauth-redirect-uris: %q uses http on a host that is not loopback", u)
		}
	}
	srv.WithGoogleSignIn(googleauth.New(googleauth.Google(f.GoogleClientID, f.GoogleClientSecret)), uris)
	if !srv.MultiTeam() {
		logger.Warn("google sign-in is configured but not offered: it needs a multi-team license")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}
