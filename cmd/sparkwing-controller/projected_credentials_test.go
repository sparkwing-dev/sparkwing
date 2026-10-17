package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func TestKubernetesControllerAndLauncherCredentials(t *testing.T) {
	directory := t.TempDir()
	version := filepath.Join(directory, "..2026_10_16")
	if err := os.Mkdir(version, 0o700); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{credStripeAPIKey: "sk_test_projected", credStripeWebhookSecret: "whsec_projected", credBootstrapAdminToken: "swk_projected", "agent-token": "swk_launcher"}
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
	creds, err := readCredentials(directory)
	if err != nil || creds.BootstrapAdminToken != "swk_projected" {
		t.Fatalf("controller projection: %v", err)
	}
	verifier, err := readPaymentAuthenticator(directory, "test")
	if err != nil || verifier == nil {
		t.Fatalf("Stripe projection: %v", err)
	}
	bearer, err := runners.FileBearer(directory)
	if err != nil {
		t.Fatalf("launcher projection: %v", err)
	}
	if got, err := bearer(t.Context()); err != nil || got != "swk_launcher" {
		t.Fatalf("launcher credential: %v", err)
	}
}
