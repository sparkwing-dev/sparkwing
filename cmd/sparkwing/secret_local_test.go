package main

import (
	"strings"
	"testing"
)

func TestSecretSetAcceptsPipelineScopeFlag(t *testing.T) {
	err := runSecretSet([]string{"--name", "DEPLOY_KEY", "--value", "secret", "--pipeline", "deploy", "--shared"})
	if err == nil || !strings.Contains(err.Error(), "--pipeline and --shared cannot be used together") {
		t.Fatalf("secrets set with --pipeline and --shared = %v, want the scope conflict", err)
	}
}
