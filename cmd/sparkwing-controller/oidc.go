package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
)

func loadOIDCIssuer(cfg deployment.Config, ttl time.Duration, log io.Writer) (*oidcissuer.Issuer, error) {
	active, published := cfg.OIDCKey().Current(), cfg.OIDCKey().Previous()
	if active == nil {
		return nil, nil
	}
	issuerURL, ok := cfg.PublicOrigin()
	if !ok {
		return nil, errors.New("an OIDC signing key needs --external-url, which becomes the token issuer")
	}
	if !strings.HasPrefix(issuerURL, "https://") {
		return nil, fmt.Errorf("the OIDC issuer %q must be https, because cloud providers fetch its keys only over TLS", issuerURL)
	}
	iss, err := oidcissuer.New(issuerURL, active, published, ttl)
	if err != nil {
		return nil, err
	}
	slog.New(controller.CredentialSafeLogHandler(slog.NewTextHandler(log, nil))).Info("OIDC issuer",
		"issuer", iss.URL(), "signing_key", iss.KeyIDs()[0], "published_keys", iss.KeyIDs(), "token_lifetime", iss.TTL())
	return iss, nil
}
