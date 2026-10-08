package main

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const (
	credPGURL                  = "pg-url"
	credSecretsKey             = "secrets-key"
	credSecretsPreviousKey     = "secrets-key.previous"
	credOIDCKey                = "oidc-key"
	credOIDCPublishedKey       = "oidc-key.published"
	credBootstrapAdminToken    = "bootstrap-admin-token"
	credLicense                = "license"
	credGitHubClientSecret     = "github-client-secret"
	credGoogleClientSecret     = "google-client-secret"
	credGitHubAppKey           = "github-app-key"
	credGitHubAppWebhookSecret = "github-app-webhook-secret"
	credCloudFrontKey          = "cloudfront-key"
	credLogsDeleteToken        = "logs-delete-token"
	credBillingToken           = "billing-token"
	credCacheToken             = authwire.CacheTokenCredential
	credCacheGrantKey          = authwire.CacheGrantKeyCredential
)

type controllerCredentials struct {
	PGURL                  string
	SecretsKey             []byte
	SecretsPreviousKey     []byte
	OIDCKey                []byte
	OIDCPublishedKey       []byte
	BootstrapAdminToken    string
	License                string
	GitHubClientSecret     string
	GoogleClientSecret     string
	GitHubAppKey           string
	GitHubAppWebhookSecret string
	CloudFrontKey          string
	LogsDeleteToken        string
	BillingToken           string
	CacheToken             string
	CacheGrantKey          string
}

func readCredentials(path string) (controllerCredentials, error) {
	var c controllerCredentials
	dir, err := credentials.Open(path)
	if err != nil {
		return c, err
	}
	for name, dst := range map[string]*string{
		credPGURL:                  &c.PGURL,
		credBootstrapAdminToken:    &c.BootstrapAdminToken,
		credLicense:                &c.License,
		credGitHubClientSecret:     &c.GitHubClientSecret,
		credGoogleClientSecret:     &c.GoogleClientSecret,
		credGitHubAppKey:           &c.GitHubAppKey,
		credGitHubAppWebhookSecret: &c.GitHubAppWebhookSecret,
		credCloudFrontKey:          &c.CloudFrontKey,
		credLogsDeleteToken:        &c.LogsDeleteToken,
		credBillingToken:           &c.BillingToken,
		credCacheToken:             &c.CacheToken,
		credCacheGrantKey:          &c.CacheGrantKey,
	} {
		if *dst, err = dir.Read(name); err != nil {
			return c, err
		}
	}
	for name, dst := range map[string]*[]byte{
		credOIDCKey:          &c.OIDCKey,
		credOIDCPublishedKey: &c.OIDCPublishedKey,
	} {
		raw, err := dir.Read(name)
		if err != nil {
			return c, err
		}
		if raw != "" {
			*dst = []byte(raw)
		}
	}
	// safety: an empty pg-url would select SQLite and serve a different
	// database from the one the operator meant, so a present file must hold a URL.
	if raw, err := dir.ReadBytes(credPGURL); err != nil {
		return c, err
	} else if raw != nil && c.PGURL == "" {
		return c, fmt.Errorf("credential %s in %s is empty; remove the file to use SQLite or write the PostgreSQL URL", credPGURL, path)
	}
	if c.SecretsKey, err = readSecretsKey(dir, credSecretsKey); err != nil {
		return c, err
	}
	if c.SecretsPreviousKey, err = readSecretsKey(dir, credSecretsPreviousKey); err != nil {
		return c, err
	}
	return c, nil
}

// safety: the key file holds either the 32 raw bytes or their base64, and
// raw bytes are taken untrimmed because whitespace is a valid key byte.
func readSecretsKey(dir credentials.Dir, name string) ([]byte, error) {
	data, err := dir.ReadBytes(name)
	if err != nil || data == nil {
		return nil, err
	}
	if len(data) == secrets.KeySize {
		return data, nil
	}
	key, err := secrets.DecodeKey(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("credential %s: %w", name, err)
	}
	return key, nil
}
