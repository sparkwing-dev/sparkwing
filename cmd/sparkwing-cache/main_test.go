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

func TestRunReadsTheGrantKeyAndControllerTokenFromTheCredentialsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, authwire.CacheGrantKeyCredential), []byte("grant-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--credentials-dir", dir, "--data-dir", t.TempDir(), "--controller", "http://controller.invalid", "--max-store-bytes", "-1"}
	err := run(args)
	if err == nil || !strings.Contains(err.Error(), authwire.CacheControllerTokenCredential) {
		t.Fatalf("run = %v, want a refusal naming %s, which only reading the grant key reaches", err, authwire.CacheControllerTokenCredential)
	}
	if err := os.WriteFile(filepath.Join(dir, authwire.CacheControllerTokenCredential), []byte("sws_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run(args)
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("run = %v, want the negative ceiling refusal, which only reading the controller token reaches", err)
	}
}

func TestRunWithoutAGrantKeyRefusesToServeOpen(t *testing.T) {
	err := run([]string{"--credentials-dir", t.TempDir(), "--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), authwire.CacheGrantKeyCredential) {
		t.Fatalf("run = %v, want a refusal naming the %s credential", err, authwire.CacheGrantKeyCredential)
	}
}
