package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
)

const (
	oidcKeyEnv          = "SPARKWING_OIDC_KEY"
	oidcPublishedKeyEnv = "SPARKWING_OIDC_PUBLISHED_KEY"
)

func loadOIDCIssuer(keyFile, publishedKeyFile, externalURL string, ttl time.Duration, log io.Writer) (*oidcissuer.Issuer, error) {
	active, err := loadOIDCKey(oidcKeyEnv, keyFile)
	if err != nil {
		return nil, err
	}
	published, err := loadOIDCKey(oidcPublishedKeyEnv, publishedKeyFile)
	if err != nil {
		return nil, err
	}
	if active == nil {
		if published != nil {
			return nil, errors.New("a published OIDC key is configured without a signing one; set " +
				oidcKeyEnv + " or --oidc-key-file to the key that signs now")
		}
		return nil, nil
	}
	issuerURL := strings.TrimRight(strings.TrimSpace(externalURL), "/")
	if issuerURL == "" {
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

// safety: the environment copy is cleared once read, so no child process
// inherits the signing key.
func loadOIDCKey(envName, filePath string) ([]byte, error) {
	v := os.Getenv(envName)
	clearEnv(envName)
	if strings.TrimSpace(v) != "" {
		return []byte(v), nil
	}
	if filePath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}
	return data, nil
}
