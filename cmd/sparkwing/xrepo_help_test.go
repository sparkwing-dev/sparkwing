package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestXrepoSubcommandsAreRegistered(t *testing.T) {
	want := map[string]bool{
		"sparkwing repos list":   false,
		"sparkwing repos add":    false,
		"sparkwing repos remove": false,
		"sparkwing repos prune":  false,
	}
	for _, command := range allCommands {
		if _, ok := want[command.Path]; ok {
			want[command.Path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("xrepo runtime command %q is absent from the command registry", path)
		}
	}
}

func TestXrepoRuntimeHelpUsesCommandRegistry(t *testing.T) {
	t.Parallel()
	bin := buildSubmitCLI(t)

	parent, err := exec.Command(bin, "repos", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("xrepo help: %v\n%s", err, parent)
	}
	if !strings.Contains(string(parent), "add     Register a checkout") {
		t.Fatalf("xrepo help does not use the registered child synopsis:\n%s", parent)
	}

	leaf, err := exec.Command(bin, "repos", "list", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("xrepo list help: %v\n%s", err, leaf)
	}
	for _, want := range []string{
		"sparkwing repos list [flags]",
		"--output FORMAT",
		"[optional]",
	} {
		if !strings.Contains(string(leaf), want) {
			t.Errorf("xrepo list help is missing %q:\n%s", want, leaf)
		}
	}

	add, err := exec.Command(bin, "repos", "add", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("xrepo add help: %v\n%s", err, add)
	}
	if !strings.Contains(string(add), "sparkwing repos add [path] [flags]") {
		t.Fatalf("xrepo add help does not render its optional path once:\n%s", add)
	}
}

func TestXrepoRegistryDescribesRuntimeInputs(t *testing.T) {
	assertFlag := func(t *testing.T, command Command, name, short string) {
		t.Helper()
		for _, spec := range command.Flags {
			if spec.Name == name {
				if spec.Short != short {
					t.Errorf("--%s short name = %q, want %q", name, spec.Short, short)
				}
				return
			}
		}
		t.Errorf("%s is missing --%s", command.Path, name)
	}

	assertFlag(t, cmdReposList, "output", "o")
	assertFlag(t, cmdReposList, "pipelines", "")

	if len(cmdReposAdd.PosArgs) != 1 || cmdReposAdd.PosArgs[0].Required {
		t.Fatalf("xrepo add positional arguments = %#v, want one optional path", cmdReposAdd.PosArgs)
	}
	if len(cmdReposRemove.PosArgs) != 1 || !cmdReposRemove.PosArgs[0].Required {
		t.Fatalf("xrepo remove positional arguments = %#v, want one required path or basename", cmdReposRemove.PosArgs)
	}
}
