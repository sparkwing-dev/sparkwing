package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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
	fmt.Fprintf(log, "sparkwing-controller: OIDC issuer %s signing with key %s, publishing %s, token lifetime %s\n",
		iss.URL(), iss.KeyIDs()[0], strings.Join(iss.KeyIDs(), ", "), iss.TTL())
	return iss, nil
}
