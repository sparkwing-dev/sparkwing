package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/license"
)

func TestInsecureCookiesRefusedForSharedControllers(t *testing.T) {
	multi, _ := secretsTestServer(t, license.FeatureMultiTeam)
	single, _ := secretsTestServer(t)
	for name, err := range map[string]error{
		"multi-team": checkInsecureCookies(multi, false, true),
		"hosted":     checkInsecureCookies(single, true, true),
	} {
		if err == nil || !strings.Contains(err.Error(), "--insecure-cookies") {
			t.Fatalf("%s controller accepted insecure cookies: %v", name, err)
		}
	}
	for name, err := range map[string]error{
		"single-team":       checkInsecureCookies(single, false, true),
		"multi-team secure": checkInsecureCookies(multi, true, false),
	} {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
