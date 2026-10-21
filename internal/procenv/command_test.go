package procenv

import (
	"runtime"
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
