package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestGitleaksGitHubInstallationTokens(t *testing.T) {
	binary, err := exec.LookPath("gitleaks")
	if err != nil {
		t.Skip("gitleaks is not installed")
	}
	for _, size := range []int{40, 520, 4096} {
		token := "ghs_" + strings.Repeat("aB1_-.", (size-4)/6) + strings.Repeat("z", (size-4)%6)
		if size == 40 {
			token = "ghs_" + strings.Repeat("aB1", 12)
		}
		cmd := exec.CommandContext(t.Context(), binary, "stdin", "--config", "../.gitleaks.toml", "--enable-rule", "github-app-token,github-installation-token", "--max-decode-depth", "0", "--exit-code", "0", "--report-format", "json", "--report-path", "-", "--no-banner", "--log-level", "error")
		cmd.Stdin = strings.NewReader("GITHUB_TOKEN=" + token + "\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("scan %d-byte token: %v", size, err)
		}
		var findings []struct{ Secret string }
		if err := json.Unmarshal(out, &findings); err != nil {
			t.Fatal(err)
		}
		if len(findings) != 1 || findings[0].Secret != token {
			t.Fatalf("scan did not report the complete %d-byte token", size)
		}
	}
}
