package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
)

func TestInsecureCookiesRefusedForSharedControllers(t *testing.T) {
	multi, hosted, single := deploymenttest.Team(t), deploymenttest.Cloud(t, "https://controller.test"), deployment.Config{}
	for name, err := range map[string]error{
		"multi-team": checkInsecureCookies(multi, true),
		"hosted":     checkInsecureCookies(hosted, true),
	} {
		if err == nil || !strings.Contains(err.Error(), "--insecure-cookies") {
			t.Fatalf("%s controller accepted insecure cookies: %v", name, err)
		}
	}
	for name, err := range map[string]error{
		"single-team":       checkInsecureCookies(single, true),
		"multi-team secure": checkInsecureCookies(multi, false),
	} {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
