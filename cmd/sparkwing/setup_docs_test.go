package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestSharedControllerSetupDocsUseProvisionedAuthority(t *testing.T) {
	for _, slug := range []string{"self-hosting", "auth", "getting-started", "deployment-modes"} {
		raw, err := docs.ReadRaw(slug)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"SPARKWING_HOME", "SPARKWING_CONFIG", "/api/v1/auth/whoami", "pending"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s omits %s", slug, want)
			}
		}
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "bin", "k8s-e2e.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `--data '{"kind":"user"`) || !strings.Contains(string(raw), "PENDING operator onboarding") {
		t.Fatal("Kubernetes setup still mints an unauthenticated admin")
	}
}
