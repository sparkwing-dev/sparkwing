package main

import (
	"strings"
	"testing"
)

func TestPRCheckScaffoldDoesNotAdvertiseDefaultRunner(t *testing.T) {
	if strings.Contains(strings.ToLower(ciPRCheckTemplate), "default runner") {
		t.Fatal("pull-request scaffold advertises the removed default runner")
	}
}
