package main

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestPrintHelpHidesHiddenFlag(t *testing.T) {
	cmd := Command{
		Path:     "sparkwing test",
		Synopsis: "test",
		Flags: []FlagSpec{
			{Name: "visible", Type: FlagString, Argument: "X", Desc: "shown"},
			{Name: "ghost", Type: FlagBool, Desc: "hidden", Hidden: true},
		},
	}
	var buf bytes.Buffer
	PrintHelp(cmd, &buf)
	out := buf.String()
	if !strings.Contains(out, "--visible") {
		t.Errorf("expected --visible in help; got:\n%s", out)
	}
	if strings.Contains(out, "--ghost") {
		t.Errorf("did not expect --ghost in help; got:\n%s", out)
	}
}

func TestRequiredProfileFlagsDoNotAdvertiseDefault(t *testing.T) {
	for _, command := range allCommands {
		for _, spec := range command.Flags {
			if spec.Name != "profile" || !spec.Required {
				continue
			}
			if spec.Default != "" {
				t.Errorf("%s requires --profile but declares default %q", command.Path, spec.Default)
			}
			if strings.Contains(strings.ToLower(spec.Desc), "default") {
				t.Errorf("%s requires --profile but describes it as %q", command.Path, spec.Desc)
			}
		}
	}
}

func TestPrintHelpDistinguishesOptionalSubcommands(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{
			name: "command group",
			cmd:  cmdQueue,
			want: "  sparkwing queue <subcommand> [flags]\n",
		},
		{
			name: "leaf with positional",
			cmd:  cmdRun,
			want: "  sparkwing run <pipeline> [flags] [-- <pipeline args>...]\n",
		},
		{
			name: "command group with flags",
			cmd:  cmdVersion,
			want: "  sparkwing version <subcommand> [flags]\n",
		},
		{
			name: "command group",
			cmd:  cmdJobs,
			want: "  sparkwing runs <subcommand> [flags]\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintHelp(tc.cmd, &buf)
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("help does not contain %q:\n%s", tc.want, buf.String())
			}
		})
	}
}

func TestCommandFlagsAreUnique(t *testing.T) {
	for _, cmd := range allCommands {
		names := make(map[string]struct{}, len(cmd.Flags))
		shorts := make(map[string]struct{}, len(cmd.Flags))
		for _, spec := range cmd.Flags {
			if _, ok := names[spec.Name]; ok {
				t.Errorf("%s declares --%s more than once", cmd.Path, spec.Name)
			}
			names[spec.Name] = struct{}{}
			if spec.Short == "" {
				continue
			}
			if _, ok := shorts[spec.Short]; ok {
				t.Errorf("%s declares -%s more than once", cmd.Path, spec.Short)
			}
			shorts[spec.Short] = struct{}{}
		}
	}
}

func TestRunsLogsHelpIncludesEventFilters(t *testing.T) {
	var buf bytes.Buffer
	PrintHelp(cmdJobsLogs, &buf)
	for _, tc := range []struct {
		name     string
		conflict string
	}{
		{name: "events-only", conflict: "no-events"},
		{name: "no-events", conflict: "events-only"},
	} {
		name := "--" + tc.name
		if !containsFlagRow(buf.String(), name) {
			t.Errorf("runs logs help omits accepted flag %s:\n%s", name, buf.String())
		}
		spec, ok := findFlagSpec(cmdJobsLogs.Flags, tc.name)
		if !ok {
			t.Errorf("runs logs registry omits accepted flag %s", name)
			continue
		}
		if !slices.Contains(spec.ConflictsWith, tc.conflict) {
			t.Errorf("%s does not declare its conflict with --%s", name, tc.conflict)
		}
	}
}

func TestRunsHelpUsesPublicLogsCommandName(t *testing.T) {
	var buf bytes.Buffer
	PrintHelp(cmdJobs, &buf)
	if strings.Contains(buf.String(), "jobs logs") {
		t.Fatalf("runs help advertises retired command name:\n%s", buf.String())
	}
}

func TestRunHelpListsArcFlags(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
	}{
		{"sparkwing run", cmdRun},
	}
	allFlags := []string{
		"--sw-ref",
		"--sw-start-at", "--sw-stop-at",
		"--sw-dry-run",
		"--target",
		"--sw-verbose",
		"--sw-allow-risk",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintHelp(tc.cmd, &buf)
			out := buf.String()
			for _, f := range allFlags {
				if !containsFlagRow(out, f) {
					t.Errorf("expected %s --help to list %s; got:\n%s", tc.name, f, out)
				}
			}
		})
	}
}

func findFlagSpec(flags []FlagSpec, name string) (FlagSpec, bool) {
	for _, spec := range flags {
		if spec.Name == name {
			return spec, true
		}
	}
	return FlagSpec{}, false
}

func containsFlagRow(out, flagName string) bool {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, flagName) {
			continue
		}
		if strings.Contains(line, "[optional]") || strings.Contains(line, "[required]") {
			return true
		}
	}
	return false
}

func TestVisibleSubcommandsHidesHiddenChild(t *testing.T) {
	for _, parent := range parentCommands() {
		visible := map[string]bool{}
		for _, s := range visibleSubcommands(parent) {
			visible[s.Name] = true
		}
		for _, child := range childCommands(parent.Path) {
			name := commandLeafName(child.Path)
			if child.Hidden && visible[name] {
				t.Errorf("parent %q: child %q is Hidden but appears in visibleSubcommands", parent.Path, name)
			}
		}
	}
}

func TestHelpListingMatchesRegistry(t *testing.T) {
	byPath := map[string]*Command{}
	for _, c := range allCommands {
		byPath[c.Path] = c
	}

	for _, parent := range allCommands {
		listed := map[string]string{}
		for _, s := range visibleSubcommands(*parent) {
			listed[s.Name] = s.Synopsis
		}

		for name, synopsis := range listed {
			child, ok := byPath[parent.Path+" "+name]
			if !ok {
				t.Errorf("%s --help offers %q, which is not a registered command",
					parent.Path, name)
				continue
			}
			if child.Hidden {
				t.Errorf("%s --help offers Hidden child %q", parent.Path, name)
			}
			if synopsis != child.Synopsis {
				t.Errorf("%s --help describes %q as %q; the command's own synopsis is %q",
					parent.Path, name, synopsis, child.Synopsis)
			}
		}

		for _, child := range childCommands(parent.Path) {
			name := commandLeafName(child.Path)
			if _, ok := listed[name]; !ok && !child.Hidden {
				t.Errorf("%s is registered but %s --help does not list it",
					child.Path, parent.Path)
			}
		}
	}
}

func TestExamplesUseOnlyDeclaredFlags(t *testing.T) {
	flagRE := regexp.MustCompile(`(?:^|\s)--([a-z][a-z0-9-]*)`)
	byPath := map[string]*Command{}
	for _, command := range allCommands {
		byPath[command.Path] = command
	}
	for _, command := range allCommands {
		for _, example := range command.Examples {
			line := stripRootFlags(example.Command)
			if i := strings.IndexAny(line, "|;&"); i >= 0 {
				line = line[:i]
			}
			var owner *Command
			path := ""
			for p, c := range byPath {
				if len(p) > len(path) && (line == p || strings.HasPrefix(line, p+" ")) {
					owner, path = c, p
				}
			}
			if path == "" || owner.UsageSuffix != "" {
				continue
			}
			declared := map[string]bool{"help": true}
			for _, spec := range owner.Flags {
				declared[spec.Name] = true
			}
			for _, m := range flagRE.FindAllStringSubmatch(strings.TrimPrefix(line, path), -1) {
				if !declared[m[1]] {
					t.Errorf("%s example %q uses undeclared --%s", owner.Path, example.Command, m[1])
				}
			}
		}
	}
}
