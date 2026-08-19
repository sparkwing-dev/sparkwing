package main

import "testing"

func TestXrepoSubcommandsAreRegistered(t *testing.T) {
	want := map[string]bool{
		"sparkwing configure xrepo list":   false,
		"sparkwing configure xrepo add":    false,
		"sparkwing configure xrepo remove": false,
		"sparkwing configure xrepo prune":  false,
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

	assertFlag(t, cmdConfigureXrepoList, "output", "o")
	assertFlag(t, cmdConfigureXrepoList, "pipelines", "")

	if len(cmdConfigureXrepoAdd.PosArgs) != 1 || cmdConfigureXrepoAdd.PosArgs[0].Required {
		t.Fatalf("xrepo add positional arguments = %#v, want one optional path", cmdConfigureXrepoAdd.PosArgs)
	}
	if len(cmdConfigureXrepoRemove.PosArgs) != 1 || !cmdConfigureXrepoRemove.PosArgs[0].Required {
		t.Fatalf("xrepo remove positional arguments = %#v, want one required path or basename", cmdConfigureXrepoRemove.PosArgs)
	}
}
