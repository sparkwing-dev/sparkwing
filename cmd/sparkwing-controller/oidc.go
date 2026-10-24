package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
)

func loadOIDCIssuer(cfg deployment.Config, ttl time.Duration) (*oidcissuer.Issuer, error) {
	if err := oidcissuer.ValidateTTL(ttl); err != nil {
		return nil, err
	}
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
	return oidcissuer.New(issuerURL, active, published, ttl)
}
