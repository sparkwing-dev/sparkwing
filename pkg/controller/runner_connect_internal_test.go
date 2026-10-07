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
	if strings.Contains(setup, " && ") {
		t.Fatalf("setup %q holds an &&, so the connect command no longer splits at its one && before the runner", setup)
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
