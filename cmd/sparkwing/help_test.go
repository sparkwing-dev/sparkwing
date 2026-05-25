package main

import (
	"bytes"
	"strings"
	"testing"

	flag "github.com/spf13/pflag"
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

func TestBindFlagsString(t *testing.T) {
	cmd := Command{
		Path: "sparkwing bind-test",
		Flags: []FlagSpec{
			{Name: "a", Type: FlagString, DefaultValue: "default-a"},
			{Name: "b", Type: FlagBool, DefaultValue: true},
			{Name: "c", Type: FlagInt, DefaultValue: 7},
			{Name: "d", Type: FlagStringSlice},
		},
	}
	fs := flag.NewFlagSet(cmd.Path, flag.ContinueOnError)
	v := bindFlags(cmd, fs)
	if err := fs.Parse([]string{"--a", "set", "--b=false", "--c", "42", "--d", "x", "--d", "y"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.String("a") != "set" {
		t.Errorf("a = %q, want %q", v.String("a"), "set")
	}
	if v.Bool("b") != false {
		t.Errorf("b = %v, want false", v.Bool("b"))
	}
	if v.Int("c") != 42 {
		t.Errorf("c = %d, want 42", v.Int("c"))
	}
	if got := v.StringSlice("d"); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("d = %v, want [x y]", got)
	}
}

func TestBindFlagsDefaults(t *testing.T) {
	cmd := Command{
		Path: "sparkwing bind-defaults",
		Flags: []FlagSpec{
			{Name: "a", Type: FlagString, DefaultValue: "default-a"},
			{Name: "b", Type: FlagBool, DefaultValue: true},
			{Name: "c", Type: FlagInt, DefaultValue: 7},
		},
	}
	fs := flag.NewFlagSet(cmd.Path, flag.ContinueOnError)
	v := bindFlags(cmd, fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.String("a") != "default-a" {
		t.Errorf("a default = %q, want default-a", v.String("a"))
	}
	if v.Bool("b") != true {
		t.Errorf("b default = %v, want true", v.Bool("b"))
	}
	if v.Int("c") != 7 {
		t.Errorf("c default = %d, want 7", v.Int("c"))
	}
}

// TestRunHelpListsArcFlags pins that `--help` on the run commands
// lists every sparkwing-owned flag (hot AND advanced). Tab completion
// curates to the hot tier; --help is the full-disclosure surface.
// The flag list is sourced from sparkwing.SparkwingFlagDocs() so a
// flag added in the SDK propagates here automatically.
func TestRunHelpListsArcFlags(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
	}{
		{"sparkwing run", cmdRun},
		{"sparkwing pipeline run", cmdPipelineRun},
	}
	allFlags := []string{
		// Hot tier
		"--sw-ref",
		"--sw-start-at", "--sw-stop-at",
		"--sw-dry-run",
		"--target", "--profile",
		// Advanced tier -- also surface in --help
		"--sw-cd", "--sw-verbose",
		"--sw-allow",
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

// TestCompletionFlagsListsHotOnly pins that tab-completion filters to
// the hot tier -- `--sw-allow` and friends only surface in
// --help, not in the completion menu.
func TestCompletionFlagsListsHotOnly(t *testing.T) {
	hotFlags := []string{
		"--sw-ref",
		"--sw-start-at", "--sw-stop-at",
		"--sw-dry-run",
		"--target", "--profile",
		"--help",
	}
	advancedFlags := []string{
		"--sw-cd", "--sw-verbose",
		"--sw-allow",
	}
	for _, tc := range []struct {
		name string
		cmd  Command
	}{
		{"sparkwing run", cmdRun},
		{"sparkwing pipeline run", cmdPipelineRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := visibleFlagsForHelp(tc.cmd, true)
			present := map[string]bool{}
			for _, f := range flags {
				present["--"+f.Name] = true
			}
			for _, f := range hotFlags {
				if !present[f] {
					t.Errorf("completion %s: expected hot flag %s; got %v", tc.name, f, flagNames(flags))
				}
			}
			for _, f := range advancedFlags {
				if present[f] {
					t.Errorf("completion %s: leaked advanced flag %s; got %v", tc.name, f, flagNames(flags))
				}
			}
		})
	}
}

func flagNames(fs []FlagSpec) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = "--" + f.Name
	}
	return out
}

// containsFlagRow returns true when out contains a help-formatted
// flag row for f -- i.e., a single line that includes both the flag
// name and an [optional]/[required] tag. Excludes mentions of the
// flag in DESCRIPTION prose where tags are absent.
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
	// Walk every parent in the registry; for each subcommand it
	// lists, the corresponding child Command (if found) reports its
	// Hidden state. A parent that lists a Hidden child must have it
	// filtered out by visibleSubcommands.
	parents := parentCommands()
	leaves := leafCommands()
	for parentKey, parent := range parents {
		visible := visibleSubcommands(parent)
		visibleNames := map[string]bool{}
		for _, s := range visible {
			visibleNames[s.Name] = true
		}
		for _, s := range parent.Subcommands {
			childKey := s.Name
			if parentKey != "" {
				childKey = parentKey + " " + s.Name
			}
			child, isLeaf := leaves[childKey]
			if !isLeaf {
				child = parents[childKey]
			}
			if child.Hidden && visibleNames[s.Name] {
				t.Errorf("parent %q: child %q is Hidden but appears in visibleSubcommands", parent.Path, s.Name)
			}
		}
	}
}
