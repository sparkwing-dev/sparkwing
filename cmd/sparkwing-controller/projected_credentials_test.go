package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func projectedCredentials(t *testing.T, values map[string]string) string {
	t.Helper()
	directory := t.TempDir()
	version := filepath.Join(directory, "..2026_10_16")
	if err := os.Mkdir(version, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(version), filepath.Join(directory, "..data")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for name, value := range values {
		if err := fssecure.WriteFile(filepath.Join(version, name), []byte(value+"\n")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func TestKubernetesControllerAndLauncherCredentials(t *testing.T) {
	directory := projectedCredentials(t, map[string]string{credStripeAPIKey: "sk_test_projected", credStripeWebhookSecret: "whsec_projected", credBootstrapAdminToken: "swk_projected"})
	creds, err := readCredentials(directory)
	if err != nil || creds.BootstrapAdminToken != "swk_projected" {
		t.Fatalf("controller projection: %v", err)
	}
	verifier, err := readPaymentAuthenticator(directory, "test")
	if err != nil || verifier == nil {
		t.Fatalf("Stripe projection: %v", err)
	}
	bearer, err := runners.FileBearer(projectedCredentials(t, map[string]string{"agent-token": "swk_launcher"}))
	if err != nil {
		t.Fatalf("launcher projection: %v", err)
	}
	if got, err := bearer(t.Context()); err != nil || got != "swk_launcher" {
		t.Fatalf("launcher credential: %v", err)
	}
}

// A Secret key the controller does not read, such as pg_url for pg-url,
// would otherwise leave its feature off and, for the store, boot an empty
// SQLite database.
func TestControllerCredentialsRefuseAnUnknownName(t *testing.T) {
	directory := projectedCredentials(t, map[string]string{"pg_url": "postgres://db/sparkwing", credLicense: "x"})
	if _, err := readCredentials(directory); err == nil || !strings.Contains(err.Error(), "pg_url") {
		t.Fatalf("readCredentials with a misnamed pg-url = %v, want a refusal naming pg_url", err)
	}
}
