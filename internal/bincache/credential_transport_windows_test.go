//go:build windows

package bincache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestWindowsCredentialTransportIsPrivateAndRemoved(t *testing.T) {
	root := filepath.Join(t.TempDir(), "space and ' quote")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	env := withPipeCredential(credentialFetchEnv(os.Environ()), "https://fixture.invalid/")
	env, extra, cleanup, err := prepareCredentialTransport(env, "fixture-user", "fixture-secret", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	if len(extra) != 0 || strings.Contains(strings.Join(env, "\n"), "fixture-secret") {
		t.Fatal("credential escaped into inherited files or environment")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("private transport directories = %d, %v", len(entries), err)
	}
	path := filepath.Join(root, entries[0].Name(), "answer")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fssecure.VerifyPrivateConfig(path, info); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "git", "credential", "fill")
		cmd.Env = env
		cmd.Stdin = strings.NewReader("protocol=https\nhost=fixture.invalid\n\n")
		body, err := cmd.Output()
		cancel()
		if err != nil || !strings.Contains(string(body), "password=fixture-secret\n") {
			t.Fatal("private credential helper failed")
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("credential residue after cleanup = %d, %v", len(entries), err)
	}
}

func TestWindowsSSHCredentialFilesArePrivateAndRemoved(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	dir, _, cleanup, err := writeSSHCredential(DirectCredential{
		Secret: "fixture-deploy-key", KnownHosts: "fixture-known-host",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	for _, name := range []string{"key", "known_hosts"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := fssecure.VerifyPrivateConfig(path, info); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("SSH credential directory survives cleanup: %v", err)
	}
}
