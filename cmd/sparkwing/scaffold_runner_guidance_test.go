package main

import (
	"strings"
	"testing"
)

func TestPRCheckScaffoldDoesNotAdvertiseDefaultRunner(t *testing.T) {
	template := strings.ToLower(ciPRCheckTemplate)
	for _, retired := range []string{"profile default", "default runner"} {
		if strings.Contains(template, retired) {
			t.Fatalf("pull-request scaffold advertises removed %q selection", retired)
		}
	}
	if !strings.Contains(template, "does not affect runner selection") {
		t.Fatal("pull-request scaffold does not state Prefers' metadata-only behavior")
	}
}
