package source

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestGitHubInstallationTokenThroughOwnerCredentialHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential helper fixture uses a POSIX shell")
	}
	token := "ghs_123_" + strings.Repeat("aB1_-.", 85) + "xy"
	dir := t.TempDir()
	g := &gitClient{
		workspace: dir, masker: secrets.NewMasker(),
		env: []string{
			"HOME=" + dir, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper",
			`GIT_CONFIG_VALUE_0=!f() { printf 'username=x-access-token\npassword=%s\n' "$FIXTURE_TOKEN"; }; f`,
			"FIXTURE_TOKEN=" + token,
		},
	}
	const remote = "https://github.com/team/repo"
	credential, err := g.ownerHTTPSCredential(t.Context(), remote)
	if err != nil || credential.Secret != token {
		t.Fatalf("credential helper did not preserve the token: %v", err)
	}
	credential.Kind = "github_app"
	g.credential = credential
	args, _, cleanup, err := g.authentication(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	config := strings.TrimPrefix(args[1], "include.path=")
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	if !strings.Contains(string(body), "Authorization: Basic "+header) {
		t.Fatal("git authentication truncated the token")
	}
	if g.masker.Mask(token) != "***" || g.masker.Mask(header) != "***" {
		t.Fatal("source authentication did not register the token and Basic header")
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(config)); !os.IsNotExist(err) {
		t.Fatal("source credential directory survived cleanup")
	}
}
