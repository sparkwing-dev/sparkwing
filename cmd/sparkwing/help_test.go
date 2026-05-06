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

// TestWingHelpListsIMPArcFlags pins IMP-039: `wing --help` and
// `sparkwing run --help` must enumerate the IMP-007/014/015 wing
// flags. The wing-flag list is sourced from sparkwing.WingFlagDocs()
// so adding a flag in the SDK surfaces it here automatically; this
// test is the regression guard.
func TestWingHelpListsIMPArcFlags(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
	}{
		{"wing", cmdWing},
		{"sparkwing run", cmdRun},
		{"sparkwing pipeline run", cmdPipelineRun},
	}
	mustContain := []string{
		// IMP-007: range-resume.
		"--start-at", "--stop-at",
		// IMP-014: dry-run.
		"--dry-run",
		// IMP-015: blast-radius escape hatches.
		"--allow-destructive", "--allow-prod", "--allow-money",
		// Pre-existing staples (regression guard).
		"--from", "--config", "--retry-of", "--on",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintHelp(tc.cmd, &buf)
			out := buf.String()
			for _, f := range mustContain {
				if !strings.Contains(out, f) {
					t.Errorf("expected %s --help to list %s; got:\n%s", tc.name, f, out)
				}
			}
		})
	}
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
