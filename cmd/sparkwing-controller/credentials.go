package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	epochDirectoryFlag         = "credential-epoch-dir"
	epochDirectoryHelp         = "writable persistent directory for credential-epoch; defaults to --credentials-dir, or the local state directory when unset"
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
	credGitHubAppPreviousHook  = "github-app-webhook-secret.previous"
	credCloudFrontKey          = "cloudfront-key"
	credBillingToken           = "billing-token"
	credStripeAPIKey           = "stripe-api-key"
	credStripeWebhookSecret    = "stripe-webhook-secret"
)

func epochMismatch(err error, dir string) error {
	if !errors.Is(err, store.ErrCredentialEpochChanged) {
		return err
	}
	return fmt.Errorf("%w: %s is missing, moved or replaced (a missing file is created afresh). Copy the deployment's original credential-epoch file into %s and start again, or run `sparkwing-controller restore prepare --sign-out-everyone` to end every session, API token and runner credential on purpose", err, filepath.Join(dir, "credential-epoch"), dir)
}

func credentialEpochDirectory(explicit, credentialsDir, home string) string {
	if explicit != "" {
		return explicit
	}
	if credentialsDir != "" {
		return credentialsDir
	}
	return home
}

// controllerCredentialNames is every file the credentials directory may
// hold; any other name is refused, so a misspelled one fails startup
// instead of reading as absent.
var controllerCredentialNames = []string{
	"credential-epoch", credPGURL, credSecretsKey, credSecretsPreviousKey, credOIDCKey, credOIDCPublishedKey,
	credBootstrapAdminToken, credLicense, credGitHubClientSecret, credGoogleClientSecret,
	credGitHubAppKey, credGitHubAppWebhookSecret, credGitHubAppPreviousHook, credCloudFrontKey,
	credBillingToken, credStripeAPIKey, credStripeWebhookSecret,
}

func readPaymentAuthenticator(dir, mode string) (capability.PaymentAuthenticator, error) {
	if mode == "" {
		if dir != "" {
			for _, name := range []string{credStripeAPIKey, credStripeWebhookSecret} {
				if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
					return nil, errors.New("Stripe credentials require --payment-mode test or live")
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("payment credential: %w", err)
				}
			}
		}
		return nil, nil
	}
	if dir == "" {
		return nil, errors.New("--payment-mode requires --credentials-dir with Stripe credentials")
	}
	return capability.NewPaymentAuthenticator(capability.PaymentConfig{
		Mode: capability.PaymentMode(mode), APIKeyFile: filepath.Join(dir, credStripeAPIKey),
		WebhookSecretFile: filepath.Join(dir, credStripeWebhookSecret),
	})
}

type controllerCredentials struct {
	PGURL                  string
	SecretsKey             []byte
	SecretsPreviousKey     []byte
	OIDCKey                []byte
	OIDCPublishedKey       []byte
	BootstrapAdminToken    string
	License                string
	GitHubError            error
	GitHubClientSecret     string
	GoogleClientSecret     string
	GitHubAppKey           string
	GitHubAppWebhookSecret string
	GitHubAppPreviousHook  string
	CloudFrontKey          string
	BillingToken           string
}

func readCredentials(path string) (controllerCredentials, error) {
	var c controllerCredentials
	dir, err := credentials.OpenNamed(path, controllerCredentialNames)
	if err != nil {
		return c, err
	}
	for name, dst := range map[string]*string{
		credPGURL:               &c.PGURL,
		credBootstrapAdminToken: &c.BootstrapAdminToken,
		credLicense:             &c.License,
		credGoogleClientSecret:  &c.GoogleClientSecret,
		credCloudFrontKey:       &c.CloudFrontKey,
		credBillingToken:        &c.BillingToken,
	} {
		if *dst, err = readCredential(dir, name); err != nil {
			return c, err
		}
	}
	for name, dst := range map[string]*string{
		credGitHubClientSecret:     &c.GitHubClientSecret,
		credGitHubAppKey:           &c.GitHubAppKey,
		credGitHubAppWebhookSecret: &c.GitHubAppWebhookSecret,
		credGitHubAppPreviousHook:  &c.GitHubAppPreviousHook,
	} {
		*dst, err = readCredential(dir, name)
		c.GitHubError = errors.Join(c.GitHubError, err)
	}
	if c.OIDCKey, err = readOIDCKey(dir); err != nil {
		return c, err
	}
	published, err := readCredential(dir, credOIDCPublishedKey)
	if err != nil {
		return c, err
	}
	if published != "" {
		c.OIDCPublishedKey = []byte(published)
	}
	if c.SecretsKey, err = readSecretsKey(dir, credSecretsKey); err != nil {
		return c, err
	}
	if c.SecretsPreviousKey, err = readSecretsKey(dir, credSecretsPreviousKey); err != nil {
		return c, err
	}
	return c, nil
}

// safety: a present but empty file is a mistake, not a way to turn a
// credential off, so its feature refuses to operate, naming the file.
func readCredential(dir credentials.Dir, name string) (string, error) {
	raw, err := dir.ReadBytes(name)
	if err != nil || raw == nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", emptyCredentialError(dir, name)
	}
	return value, nil
}

func emptyCredentialError(dir credentials.Dir, name string) error {
	return fmt.Errorf("credential %s in %s is empty; write its value or remove the file to leave it unset", name, dir.Path())
}

func readOIDCKey(dir credentials.Dir) ([]byte, error) {
	raw, err := dir.ReadPrivateBytes(credOIDCKey)
	if err != nil || raw == nil {
		return nil, err
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return nil, emptyCredentialError(dir, credOIDCKey)
	}
	return []byte(key), nil
}

// safety: the key file holds either the 32 raw bytes or their base64, and
// raw bytes are taken untrimmed because whitespace is a valid key byte.
func readSecretsKey(dir credentials.Dir, name string) ([]byte, error) {
	data, err := dir.ReadBytes(name)
	if err != nil || data == nil {
		return nil, err
	}
	if len(data) != secrets.KeySize && strings.TrimSpace(string(data)) == "" {
		return nil, emptyCredentialError(dir, name)
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
