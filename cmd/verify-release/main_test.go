package main

import (
	"strings"
	"testing"
)

func TestRunVerifyDoesNotLoadThePrivateSigningKey(t *testing.T) {
	t.Setenv("SPARKWING_RELEASE_SIGNING_KEY", "")
	err := run([]string{"--verify", "--dist", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "release asset set mismatch") {
		t.Fatalf("verification without a signing key failed at the wrong boundary: %v", err)
	}
}

func TestRunSigningModesStillRequireThePrivateKey(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "sign", args: []string{"--dist", t.TempDir()}},
		{name: "public key", args: []string{"--public-key"}},
		{name: "public key with verify", args: []string{"--public-key", "--verify"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SPARKWING_RELEASE_SIGNING_KEY", "")
			err := run(test.args)
			if err == nil || !strings.Contains(err.Error(), "release signing key is 0 bytes") {
				t.Fatalf("mode did not require private signing material: %v", err)
			}
		})
	}
}
