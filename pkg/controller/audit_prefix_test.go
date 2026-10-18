package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestMissingPathRedactsEveryCredentialPrefix(t *testing.T) {
	if len(capability.CredentialPrefixes) < 3 {
		t.Fatalf("credential prefixes = %v", capability.CredentialPrefixes)
	}
	for _, prefix := range capability.CredentialPrefixes {
		got := sanitizedMissingPath("/missing/"+prefix+"Opaque_Secret-123", func(s string) string { return s })
		if got != "/missing/%5Bredacted%5D" {
			t.Errorf("%s token path logged as %q", prefix, got)
		}
	}
}
