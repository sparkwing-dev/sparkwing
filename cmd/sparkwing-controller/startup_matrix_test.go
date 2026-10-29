package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// Every row is a misdeclared deployment. Each must refuse before the store is
// opened, so it neither starts in a weaker mode nor creates or changes anything.
func TestStartupMatrixRefusesBeforeTouchingTheStore(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	for _, tc := range []struct {
		name  string
		files map[string]string
		args  []string
		want  string
	}{
		{"misnamed pg-url", map[string]string{"pg_url": "postgres://db/sparkwing"}, nil, "pg_url"},
		{"postgres without pg-url", nil, []string{"--store=postgres"}, "pg-url"},
		{"pg-url under sqlite", map[string]string{credPGURL: "postgres://db/sparkwing"}, nil, "--store=postgres"},
		{"cloud on sqlite", nil, []string{"--deployment=cloud"}, "--store=postgres"},
		{"team without a license", nil, []string{"--deployment=team"}, "license"},
		{"unverifiable license", map[string]string{credLicense: "garbage"}, []string{"--deployment=team"}, "license credential"},
		{"unverifiable license on a local deployment", map[string]string{credLicense: "garbage"}, nil, "license credential"},
		{"unknown kind", nil, []string{"--deployment=teams"}, "--deployment"},
		{"unknown store", nil, []string{"--store=postgresql"}, "--store"},
		{"cloud limits off the cloud", nil, []string{"--limits-profile=cloud"}, "--deployment=cloud"},
		{"credit sales without settlement", map[string]string{credBillingToken: "token"}, []string{"--billing-url=https://checkout.example"}, "--payment-mode"},
		{
			"payment mode, then a bad flag",
			map[string]string{credStripeAPIKey: "sk_test_x", credStripeWebhookSecret: "whsec_x"},
			[]string{"--payment-mode=test", "--bucket-measure-pages=0"},
			"--bucket-measure-pages",
		},
		{"external origin runners would refuse", nil, []string{"--external-url=http://controller.example.com"}, "unencrypted"},
		{
			"malformed listen address",
			map[string]string{credStripeAPIKey: "sk_test_x", credStripeWebhookSecret: "whsec_x"},
			[]string{"--payment-mode=test", "--addr=nope"},
			"--addr",
		},
		{"empty API address", nil, []string{"--addr="}, "--addr"},
		{"occupied port", nil, []string{"--addr=" + taken.Addr().String()}, "--addr"},
		{"occupied metrics port", nil, []string{"--metrics-addr=" + taken.Addr().String()}, "--metrics-addr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", t.TempDir())
			dir := t.TempDir()
			for name, value := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"--addr=127.0.0.1:0", "--credentials-dir=" + dir}, tc.args...)
			err := run(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%q) = %v, want a refusal naming %q", args, err, tc.want)
			}
			p, perr := paths.DefaultPaths()
			if perr != nil {
				t.Fatal(perr)
			}
			if _, serr := os.Stat(p.StateDB()); !errors.Is(serr, os.ErrNotExist) {
				t.Fatalf("a refused start opened the store: %v", serr)
			}
		})
	}
}

func TestEmptyAPIAddressRefusesBeforeOpeningStore(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if err := run([]string{"--addr="}); err == nil || !strings.Contains(err.Error(), "--addr") {
		t.Fatalf("empty --addr: %v", err)
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty --addr opened the store: %v", err)
	}
}

func TestStartupConfigurationValidationPrecedesRuntime(t *testing.T) {
	key := string(oidcTestKey(t))
	for _, tc := range []struct {
		name  string
		files map[string]string
		args  []string
		want  string
	}{
		{"signup gate", nil, []string{"--signup-gate=invalid"}, "--signup-gate"},
		{"Google client without secret", nil, []string{"--google-client-id=client"}, "Google sign-in"},
		{"Google secret without client", map[string]string{credGoogleClientSecret: "secret"}, nil, "Google sign-in"},
		{"Google without redirects", map[string]string{credGoogleClientSecret: "secret"}, []string{"--google-client-id=client"}, "--oauth-redirect-uris"},
		{"invalid redirect without provider", nil, []string{"--oauth-redirect-uris=http://example.com/callback"}, "--oauth-redirect-uris"},
		{"invalid lifetime without key", nil, []string{"--oidc-token-ttl=2h"}, "token lifetime"},
		{"Google invalid redirect", map[string]string{credGoogleClientSecret: "secret"}, []string{"--google-client-id=client", "--oauth-redirect-uris=http://example.com/callback"}, "--oauth-redirect-uris"},
		{"OIDC without origin", map[string]string{credOIDCKey: key}, nil, "--external-url"},
		{"invalid OIDC lifetime", map[string]string{credOIDCKey: key}, []string{"--external-url=https://example.com", "--oidc-token-ttl=2h"}, "token lifetime"},
		{"invalid log format", nil, []string{"--log-format=invalid"}, "--log-format"},
		{"invalid log level", nil, []string{"--log-level=invalid"}, "--log-level"},
		{"invalid OIDC key", map[string]string{credOIDCKey: "bad key"}, []string{"--external-url=https://example.com"}, "signing key"},
		{"invalid bucket URL", nil, []string{"--bucket-store=fs://relative"}, "--bucket-store"},
		{"invalid cache URL", nil, []string{"--cache-blob-store=s3://"}, "--cache-blob-store"},
		{"partial CloudFront", nil, []string{"--cache-blob-store=s3://bucket", "--cloudfront-domain=downloads.example.com"}, "CloudFront"},
		{"invalid object budget", nil, []string{"--object-store-budget=nope"}, "--object-store-budget"},
		{"negative bucket ceiling", nil, []string{"--max-bucket-bytes=-1"}, "--max-bucket-bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "home")
			epoch := filepath.Join(t.TempDir(), "epoch")
			t.Setenv("SPARKWING_HOME", root)
			dir := credentialsDir(t, tc.files)
			args := append([]string{"--addr=127.0.0.1:0", "--credentials-dir=" + dir, "--credential-epoch-dir=" + epoch}, tc.args...)
			if err := run(args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%q) = %v, want %q", args, err, tc.want)
			}
			for _, path := range []string{root, epoch} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid configuration touched %s: %v", path, err)
				}
			}
		})
	}
}
