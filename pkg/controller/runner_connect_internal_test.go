package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func TestRunnerConnectArgsShipsLogsOnlyToAnAnnouncedLogsService(t *testing.T) {
	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	got := runnerConnectArgs("https://api.example", "https://logs.example/", "box", allow)
	want := `sparkwing-runner runner --credentials-dir "$HOME/.config/sparkwing/runner-credentials"` +
		" --controller https://api.example --logs https://logs.example" +
		" --allow-repo 'github.com/acme/*' --also-claim-triggers --max-claims-before-restart 0 --metrics-addr= --holder-prefix box"
	if got != want {
		t.Fatalf("runnerConnectArgs = %q\nwant %q", got, want)
	}
	got = runnerConnectArgs("https://api.example", "", "box", allow)
	want = `sparkwing-runner runner --credentials-dir "$HOME/.config/sparkwing/runner-credentials"` +
		" --controller https://api.example" +
		" --allow-repo 'github.com/acme/*' --also-claim-triggers --max-claims-before-restart 0 --metrics-addr= --holder-prefix box"
	if got != want {
		t.Fatalf("runnerConnectArgs without a logs service = %q\nwant %q", got, want)
	}
}

func TestRunnerTokenSetupReplacesAnOpenTokenFileWithAPrivateOne(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "sparkwing", "runner-credentials")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agent-token")
	if err := os.WriteFile(file, []byte("swr_old"), 0o644); err != nil {
		t.Fatal(err)
	}
	setup := runnerTokenSetup("swr_new")
	if strings.Contains(setup, ") && ") {
		t.Fatalf("setup %q holds \") && \", where the connect command splits before the runner", setup)
	}
	cmd := exec.Command("sh", "-c", setup)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("agent-token mode = %o, want 600 even when a readable file was there before", mode)
	}
	if got, _ := os.ReadFile(file); string(got) != "swr_new" {
		t.Errorf("agent-token = %q, want the new token", got)
	}
}

func TestTheConnectCommandStopsWhenItCannotReplaceTheTokenFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a file from a read-only directory")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "sparkwing", "runner-credentials")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agent-token")
	if err := os.WriteFile(file, []byte("swr_old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	bin := t.TempDir()
	started := filepath.Join(bin, "started")
	stub := "#!/bin/sh\ntouch " + started + "\n"
	if err := os.WriteFile(filepath.Join(bin, "sparkwing-runner"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", runnerTokenSetup("swr_new")+" && "+runnerConnectArgs("https://api.example", "", "box", allow))
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("connect command succeeded although it could not remove the old token file:\n%s", out)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("the runner started after the token setup failed")
	}
	if got, _ := os.ReadFile(file); string(got) != "swr_old" {
		t.Errorf("agent-token = %q, want the 0644 file left untouched rather than holding the new token", got)
	}
}
