package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
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

func TestProfilesRegistryMatchesDispatcher(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "profiles.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	dispatched := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runProfiles" {
			return true
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			clause, ok := node.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				literal, ok := expr.(*ast.BasicLit)
				if ok && literal.Kind == token.STRING {
					dispatched[strings.Trim(literal.Value, `"`)] = true
				}
			}
			return true
		})
		return false
	})

	for _, child := range childCommands(cmdProfiles.Path) {
		name := commandLeafName(child.Path)
		if !child.Hidden && !dispatched[name] {
			t.Errorf("%s is registered but runProfiles does not dispatch %q", child.Path, name)
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
			name: "runnable parent",
			cmd:  cmdQueue,
			want: "  sparkwing queue [<subcommand>] [flags]\n",
		},
		{
			name: "runnable parent with positional",
			cmd:  cmdRun,
			want: "  sparkwing run <pipeline> [<subcommand>] [flags] [-- pipeline-flags...]\n",
		},
		{
			name: "runnable parent with flags",
			cmd:  cmdVersion,
			want: "  sparkwing version [<subcommand>] [flags]\n",
		},
		{
			name: "runnable parent with default action",
			cmd:  cmdRepos,
			want: "  sparkwing repos [<subcommand>] [flags]\n",
		},
		{
			name: "command group",
			cmd:  cmdJobs,
			want: "  sparkwing runs <subcommand>\n",
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
		"--sw-ref",
		"--sw-start-at", "--sw-stop-at",
		"--sw-dry-run",
		"--target", "--profile",
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

// TestHelpListingMatchesRegistry is the acceptance criterion for this
// surface, asserted against the rendered listing rather than against
// the field it is derived from: every subcommand a group's help offers
// resolves to a registered Command, every non-Hidden registered child
// is offered, and each row carries that child's own synopsis.
//
// A group's help used to be a hand-written twin of the registry, and a
// twin drifts silently: help named an `xrepo` with no Command, omitted
// the registered `examples` and `run config`, and reworded seventy
// synopses on one side only. A reader has no way to tell a stale
// listing from a true one, which is what makes the defect expensive.
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

// TestSubcommandOrderMatchesRegistry keeps the ordering hint honest.
// A stale name in it cannot corrupt the listing -- filterSubcommands
// ignores what does not resolve and appends what the hint forgot -- so
// nothing user-facing breaks when it rots, which is exactly why it
// needs its own gate: silent rot is how the old hand-written listing
// got as far out of date as it did.
func TestSubcommandOrderMatchesRegistry(t *testing.T) {
	for _, parent := range allCommands {
		want := map[string]bool{}
		for _, child := range childCommands(parent.Path) {
			if !child.Hidden {
				want[commandLeafName(child.Path)] = true
			}
		}

		seen := map[string]bool{}
		for _, name := range parent.SubcommandOrder {
			switch {
			case seen[name]:
				t.Errorf("%s SubcommandOrder names %q twice", parent.Path, name)
			case !want[name]:
				t.Errorf("%s SubcommandOrder names %q, which is not a visible registered child of it",
					parent.Path, name)
			}
			seen[name] = true
		}
		for name := range want {
			if !seen[name] {
				t.Errorf("%s SubcommandOrder omits its registered child %q, so the child's place in the listing is unowned",
					parent.Path, name)
			}
		}
	}
}

// A value-taking flag consumes whatever follows it, so the parser binds
// `--template --help` as a template named "--help" and then complains
// about an unrelated missing flag. Someone reaching for --help does not
// know the flag grammar yet, which is why they are asking, so the
// request is answered before parsing.
func TestWantsHelpIsPositionIndependent(t *testing.T) {
	yes := [][]string{
		{"--help"},
		{"-h"},
		{"--template", "--help"},
		{"--name", "x", "--template", "--help"},
		{"--topic", "-h"},
	}
	for _, args := range yes {
		if !wantsHelp(args) {
			t.Errorf("wantsHelp(%q) = false, want true", args)
		}
	}

	no := [][]string{
		{},
		{"--name", "x"},
		{"--short", "helpful things"},
		{"--query", "help"},
	}
	for _, args := range no {
		if wantsHelp(args) {
			t.Errorf("wantsHelp(%q) = true, want false", args)
		}
	}
}

// Everything after `--` is an operand. A pipeline argument spelled
// --help belongs to the pipeline, not to sparkwing.
func TestWantsHelpStopsAtTheTerminator(t *testing.T) {
	if wantsHelp([]string{"--", "--help"}) {
		t.Error("--help after -- is an operand, not a help request")
	}
	if !wantsHelp([]string{"--help", "--", "x"}) {
		t.Error("--help before -- is still a help request")
	}
}
