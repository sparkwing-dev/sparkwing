package pipelineentry

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallAppliesCredentialPolicyAndMasksOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	dir := t.TempDir()
	npm := filepath.Join(dir, "npm")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s' \"$AWS_SECRET_ACCESS_KEY\" \"$GH_TOKEN\" \"$CUSTOM_API_TOKEN\" \"$HTTPS_PROXY\"\nprintf '%s' proxy-private-password >&2\n"
	if err := os.WriteFile(npm, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var output bytes.Buffer
	env := []string{"AWS_SECRET_ACCESS_KEY=aws-private", "GH_TOKEN=gh-private", "CUSTOM_API_TOKEN=custom-private", "HTTPS_PROXY=https://alice:proxy-private-password@proxy.example"}
	if err := InstallTypeScript(t.Context(), dir, env, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "|||https://***@proxy.example***" {
		t.Fatalf("install did not filter and mask output: %q", got)
	}
	for _, value := range []string{"aws-private", "gh-private", "custom-private", "proxy-private-password", "alice"} {
		if strings.Contains(output.String(), value) {
			t.Errorf("credential reached output: %s", value)
		}
	}
}
