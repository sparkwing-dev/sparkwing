package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPaymentConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, mode, key, secret string
		want, invalid           bool
	}{
		{name: "unset"},
		{name: "test", mode: "test", key: "sk_test_synthetic", secret: "whsec_synthetic", want: true},
		{name: "live", mode: "live", key: "rk_live_synthetic", secret: "whsec_synthetic", want: true},
		{name: "mode absent", key: "sk_test_synthetic", secret: "whsec_synthetic", invalid: true},
		{name: "unknown mode", mode: "testing", key: "sk_test_synthetic", secret: "whsec_synthetic", invalid: true},
		{name: "wrong key mode", mode: "live", key: "sk_test_synthetic", secret: "whsec_synthetic", invalid: true},
		{name: "missing key", mode: "test", secret: "whsec_synthetic", invalid: true},
		{name: "missing webhook secret", mode: "test", key: "sk_test_synthetic", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, value := range map[string]string{credStripeAPIKey: tc.key, credStripeWebhookSecret: tc.secret} {
				if value != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			verifier, err := readPaymentAuthenticator(dir, tc.mode)
			if (err != nil) != tc.invalid || (verifier != nil) != tc.want {
				t.Fatalf("configured=%v, error=%v", verifier != nil, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic") {
				t.Fatal("configuration error disclosed a credential")
			}
		})
	}
	if _, err := readPaymentAuthenticator("", "test"); err == nil {
		t.Fatal("payment mode without credential directory was accepted")
	}
	if verifier, err := readPaymentAuthenticator("", ""); verifier != nil || err != nil {
		t.Fatal("absent payment configuration did not stay disabled")
	}
}

func TestPaymentDeploymentNamesTheExternalHost(t *testing.T) {
	for _, tc := range []struct{ external, billing, mode, want string }{
		{"https://Staging.Sparkwing.dev:8443/x", "http://checkout", "", "staging.sparkwing.dev"},
		{"https://api.sparkwing.dev", "", "test", "api.sparkwing.dev"},
		{"", "", "", ""},
	} {
		got, err := paymentDeployment(tc.external, tc.billing, tc.mode)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %q %v", tc, got, err)
		}
	}
	if _, err := paymentDeployment("", "http://checkout", ""); err == nil {
		t.Fatal("a billing deployment without --external-url started")
	}
}
