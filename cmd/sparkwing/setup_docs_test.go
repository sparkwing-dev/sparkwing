package main

import (
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
		for _, want := range []string{"SPARKWING_HOME", "SPARKWING_CONFIG", "/api/v1/auth/whoami", "sparkwing operator teams create"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s omits %s", slug, want)
			}
		}
	}
}
