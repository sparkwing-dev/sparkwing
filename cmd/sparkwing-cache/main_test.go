package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

func TestRunRefusesAMalformedFlagValue(t *testing.T) {
	for _, args := range [][]string{
		{"--fetch-interval", "5 minutes"},
		{"--allow-unauthenticated=yes"},
		{"--max-store-bytes", "10GiB"},
	} {
		err := run(args)
		if err == nil {
			t.Fatalf("run(%q) started with a value it cannot read", args)
		}
		if name := strings.TrimPrefix(strings.SplitN(args[0], "=", 2)[0], "--"); !strings.Contains(err.Error(), name) {
			t.Errorf("run(%q) = %v, want it to name %s", args, err, name)
		}
	}
}

func TestRunReadsTheTokenAndGrantKeyFromTheCredentialsDir(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{authwire.CacheTokenCredential, authwire.CacheGrantKeyCredential} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("same-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := run([]string{"--credentials-dir", dir, "--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "operator token") {
		t.Fatalf("run = %v, want the refusal of a grant key equal to the token, which only reading both files reaches", err)
	}
}

func TestRunWithoutATokenRefusesToServeOpen(t *testing.T) {
	err := run([]string{"--credentials-dir", t.TempDir(), "--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), authwire.CacheTokenCredential) {
		t.Fatalf("run = %v, want a refusal naming the %s credential", err, authwire.CacheTokenCredential)
	}
}
