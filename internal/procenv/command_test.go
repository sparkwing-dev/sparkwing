package procenv

import (
	"runtime"
	"strings"
	"testing"
)

func TestCommandOwnsExplicitEnvironment(t *testing.T) {
	for _, environment := range [][]string{nil, {}, {"A=one"}} {
		command := Command(Env{values: environment}, "unused")
		if command.Env == nil {
			t.Fatal("nil environment would inherit host credentials")
		}
		if len(environment) > 0 {
			environment[0] = "A=changed"
			if command.Env[0] != "A=one" {
				t.Fatal("environment aliases caller memory")
			}
		}
	}
	command := CommandContext(t.Context(), Env{}, "unused")
	if command.Env == nil {
		t.Fatal("context command inherits host environment")
	}
}

func TestHostCommandPreservesDirectoryEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PWD is a Unix environment convention")
	}
	directory := t.TempDir()
	command := HostCommand("/bin/sh", "-c", `printf %s "$PWD"`)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != directory {
		t.Fatalf("PWD=%q, want %q", output, directory)
	}
}

func TestSDKCommandDropsOnlyEngineCredentials(t *testing.T) {
	for _, row := range Registry {
		if row.Class == Credential && row.Owner == "engine" && !strings.HasPrefix(row.Name, engineCredentialPrefix) {
			t.Fatalf("engine credential %s escapes the SDKCommand prefix check", row.Name)
		}
	}
	cmd := SDKCommand([]string{"PATH=/bin", "SPARKWING_NODE_TOKEN=minted", "SPARKWING_SECRETS_KEY=k", "GITHUB_TOKEN=user", "SPARKWING_HOME=/h"}, "true")
	if got := strings.Join(cmd.Env, ","); got != "PATH=/bin,GITHUB_TOKEN=user,SPARKWING_HOME=/h" {
		t.Fatalf("SDKCommand env = %s", got)
	}
}
