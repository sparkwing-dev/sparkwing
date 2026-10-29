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
		"does not affect runner selection",
	} {
		if strings.Contains(template, falseClaim) {
			t.Errorf("pull-request scaffold contains false claim %q", falseClaim)
		}
	}
	if !strings.Contains(template, "head start without") {
		t.Fatal("pull-request scaffold does not state that Prefers ranks runners without relaxing placement")
	}
}
