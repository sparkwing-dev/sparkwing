package main

import (
	"strings"
	"testing"
)

func TestPRCheckScaffoldDoesNotAdvertiseDefaultRunner(t *testing.T) {
	template := strings.ToLower(ciPRCheckTemplate)
	for _, falseClaim := range []string{
		"profile default",
		"default runner",
		"biases runner selection",
		"dispatch snapshot",
		"renderer",
		"dashboard",
	} {
		if strings.Contains(template, falseClaim) {
			t.Errorf("pull-request scaffold contains false claim %q", falseClaim)
		}
	}
	if !strings.Contains(template, "plan-snapshot metadata") {
		t.Fatal("pull-request scaffold does not identify the stored preference metadata")
	}
	if !strings.Contains(template, "does not affect runner selection") {
		t.Fatal("pull-request scaffold does not state Prefers' metadata-only behavior")
	}
}
