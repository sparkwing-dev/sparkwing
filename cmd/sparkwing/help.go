// Hand-rolled help + flag-validation framework. Each leaf command
// declares its shape as a Command in help_registry.go; its handler
// calls parseAndCheck to parse flags + validate dependencies.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"
)

// FlagType is the typed-binding hint bindFlags reads. Empty = untyped
// (bindFlags skips it so handlers can register manually with pflag).
type FlagType string

const (
	FlagString      FlagType = "string"
	FlagBool        FlagType = "bool"
	FlagInt         FlagType = "int"
	FlagInt64       FlagType = "int64"
	FlagDuration    FlagType = "duration"
	FlagStringSlice FlagType = "stringSlice"
)

// FlagSpec is metadata for one CLI flag. The handler still owns the
// pflag registration; FlagSpec feeds help, dep-checking, and completion.
type FlagSpec struct {
	Name     string
	Short    string
	Argument string // "<name>" for value-taking flags, "" for booleans
	Desc     string
	Group    string // "Input", "Filter", "Output", "System", "Other"

	Required      bool
	RequiredWhen  string
	RequiresFlags []string
	ConflictsWith []string

	Default string

	// Type opts a flag into auto-registration via bindFlags.
	Type         FlagType
	DefaultValue any

	// Hidden flags are parsed/validated but absent from help and completion.
	Hidden bool

	// Hot marks the flag as part of the default --help / tab-completion
	// surface. When any flag on a command is Hot, the default help
	// renders only Hot flags (plus --help / --help-all). --help-all
	// bypasses the filter and shows everything.
	Hot bool
}

type PosArg struct {
	Name     string // includes brackets, e.g. "<pipeline>"
	Desc     string
	Required bool
}

type Example struct {
	Desc    string
	Command string
}

// SubcommandRef is one row of a group's COMMANDS listing. It is
// always derived from the registry by filterSubcommands, never
// authored: see Command.SubcommandOrder.
type SubcommandRef struct {
	Name     string
	Synopsis string
}

type Command struct {
	Path        string
	Synopsis    string
	Description string

	// SubcommandOrder is a display-order hint for this group's
	// children, not the child list. The list itself is derived from
	// the registry, so a group's help can neither name a command the
	// CLI does not dispatch nor omit one it does -- the two used to be
	// separate facts and had drifted in both directions.
	//
	// A name here that resolves to nothing is ignored, and a
	// registered child missing from it is still listed (appended in
	// registry order), so the hint can only ever reorder a correct
	// listing. TestSubcommandOrderMatchesRegistry fails on either kind
	// of staleness so it does not quietly accumulate.
	//
	// It exists because the order is editorial and the registry cannot
	// express it: the root menu leads with `info` because that is the
	// command an agent should run first, and `runs` leads with
	// `submit` / `list` rather than alphabetically.
	SubcommandOrder []string

	PosArgs     []PosArg
	Flags       []FlagSpec
	Examples    []Example
	GroupOrder  []string
	UsageSuffix string

	// Hidden = omit from COMMANDS listing + tab-complete; still dispatchable.
	Hidden bool
	// HideFromComplete = visible in --help, suppressed from tab-complete.
	HideFromComplete bool
}

var helpFlag = FlagSpec{
	Name:  "help",
	Short: "h",
	Desc:  "Show help for this command (including hidden flags) and exit",
	Group: "Other",
	Hot:   true,
}

// errHelpRequested = the user passed -h / --help; handlers should bail.
var errHelpRequested = errors.New("help requested")

// parseAndCheck injects --help, parses args, and validates flag deps.
// wantsHelp reports whether args contain a bare help request.
//
// It is checked before parsing, because parsing can swallow the
// request: a value-taking flag consumes whatever follows it, so
// `pipeline new --template --help` binds --help as the template name
// and then fails on a missing --name. Someone reaching for --help does
// not know the flag grammar yet -- that is why they are asking -- so it
// cannot be the thing they have to get right first.
//
// Everything after a `--` terminator is an operand, not a flag, so a
// pipeline argument that happens to be spelled --help is left alone.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// declaredFlags is the set of long flag names this command documents,
// used to let a command reclaim a retired global spelling.
func (c Command) declaredFlags() map[string]bool {
	if len(c.Flags) == 0 {
		return nil
	}
	out := make(map[string]bool, len(c.Flags))
	for _, f := range c.Flags {
		out[f.Name] = true
	}
	return out
}

func parseAndCheck(cmd Command, fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)

	// A command that declares a flag in its own registry entry owns
	// that spelling, retired global or not. `pipeline new --on` names
	// the sparkwing.yaml `on:` key it writes, which is the whole reason
	// to spell it that way; the retired `--on` addressed a profile, so
	// the two never take the same values and a stale invocation lands on
	// this command's own "unknown trigger" error rather than a wrong
	// scaffold.
	if err := checkRetiredWhereFlags(args, cmd.declaredFlags()); err != nil {
		return err
	}

	if wantsHelp(args) {
		renderHelp(cmd, args, os.Stdout)
		return errHelpRequested
	}

	if fs.Lookup("help") == nil {
		fs.BoolP("help", "h", false, helpFlag.Desc)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			renderHelp(cmd, args, os.Stdout)
			return errHelpRequested
		}
		PrintHelp(cmd, os.Stderr)
		return fmt.Errorf("%s: %w", cmd.Path, err)
	}

	if v, err := fs.GetBool("help"); err == nil && v {
		renderHelp(cmd, args, os.Stdout)
		return errHelpRequested
	}

	return validateFlagDeps(cmd, fs)
}

func validateFlagDeps(cmd Command, fs *flag.FlagSet) error {
	for _, spec := range cmd.Flags {
		if fs.Lookup(spec.Name) == nil {
			continue
		}
		changed := fs.Changed(spec.Name)
		if spec.Required && !changed {
			return fmt.Errorf("%s: --%s is required", cmd.Path, spec.Name)
		}
		if !changed {
			continue
		}
		for _, req := range spec.RequiresFlags {
			if fs.Lookup(req) == nil || !fs.Changed(req) {
				return fmt.Errorf(
					"%s: --%s was set but --%s is required with it",
					cmd.Path, spec.Name, req,
				)
			}
		}
		for _, c := range spec.ConflictsWith {
			if fs.Lookup(c) != nil && fs.Changed(c) {
				return fmt.Errorf(
					"%s: --%s and --%s cannot be used together",
					cmd.Path, spec.Name, c,
				)
			}
		}
	}
	return nil
}

func PrintHelp(cmd Command, w io.Writer) {
	printHelpWithFlags(cmd, w, visibleFlagsForHelp(cmd, false))
}

func printHelpWithFlags(cmd Command, w io.Writer, flags []FlagSpec) {
	// The listing drives USAGE too: a group whose only child is
	// Hidden offers no subcommand to type, so it must not advertise
	// one.
	visible := visibleSubcommands(cmd)

	if cmd.Synopsis != "" {
		fmt.Fprintln(w, cmd.Synopsis)
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "USAGE")
	fmt.Fprint(w, "  ", cmd.Path)
	for _, a := range cmd.PosArgs {
		if a.Required {
			fmt.Fprint(w, " ", a.Name)
		} else {
			fmt.Fprint(w, " [", a.Name, "]")
		}
	}
	if len(visible) > 0 {
		fmt.Fprint(w, " <subcommand>")
	}
	if len(cmd.Flags) > 0 || len(visible) == 0 {
		fmt.Fprint(w, " [flags]")
	}
	if cmd.UsageSuffix != "" {
		fmt.Fprint(w, " ", cmd.UsageSuffix)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	if cmd.Description != "" {
		fmt.Fprintln(w, "DESCRIPTION")
		for _, line := range strings.Split(strings.TrimRight(cmd.Description, "\n"), "\n") {
			fmt.Fprint(w, "  ", line, "\n")
		}
		fmt.Fprintln(w)
	}

	if len(visible) > 0 {
		fmt.Fprintln(w, "COMMANDS")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, s := range visible {
			fmt.Fprint(tw, "  ", s.Name, "\t", s.Synopsis, "\n")
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	if len(cmd.PosArgs) > 0 {
		fmt.Fprintln(w, "ARGUMENTS")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, a := range cmd.PosArgs {
			tag := "[optional]"
			if a.Required {
				tag = "[required]"
			}
			fmt.Fprint(tw, "  ", a.Name, "\t", tag, "\t", a.Desc, "\n")
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	if len(flags) > 0 {
		fmt.Fprintln(w, "FLAGS")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, f := range flags {
			fmt.Fprint(tw, "  ", formatFlagLHS(f), "\t", formatFlagTags(f), "\t", f.Desc, "\n")
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	if len(cmd.Examples) > 0 {
		fmt.Fprintln(w, "EXAMPLES")
		for i, ex := range cmd.Examples {
			if ex.Desc != "" {
				fmt.Fprint(w, "  # ", ex.Desc, "\n")
			}
			fmt.Fprint(w, "  ", ex.Command, "\n")
			if i < len(cmd.Examples)-1 {
				fmt.Fprintln(w)
			}
		}
		fmt.Fprintln(w)
	}
}

func formatFlagLHS(f FlagSpec) string {
	var b strings.Builder
	if f.Short != "" {
		b.WriteString("-")
		b.WriteString(f.Short)
		b.WriteString(", ")
	} else {
		b.WriteString("    ")
	}
	b.WriteString("--")
	b.WriteString(f.Name)
	if f.Argument != "" {
		b.WriteString(" ")
		b.WriteString(f.Argument)
	}
	return b.String()
}

func formatFlagTags(f FlagSpec) string {
	var parts []string
	switch {
	case f.Required:
		parts = append(parts, "[required]")
	case f.RequiredWhen != "":
		parts = append(parts, "[required "+f.RequiredWhen+"]")
	default:
		parts = append(parts, "[optional]")
	}
	if len(f.RequiresFlags) > 0 {
		parts = append(parts, "(implies --"+strings.Join(f.RequiresFlags, ", --")+")")
	}
	if len(f.ConflictsWith) > 0 {
		parts = append(parts, "(vs --"+strings.Join(f.ConflictsWith, ", --")+")")
	}
	if f.Default != "" {
		parts = append(parts, "(default: "+f.Default+")")
	}
	return strings.Join(parts, " ")
}

func visibleSubcommands(parent Command) []SubcommandRef {
	return filterSubcommands(parent, false)
}

// completableSubcommands additionally drops HideFromComplete entries.
func completableSubcommands(parent Command) []SubcommandRef {
	return filterSubcommands(parent, true)
}

// filterSubcommands derives a group's listing from the registry:
// every Command registered directly under parent.Path, named and
// described by that Command itself, ordered by parent.SubcommandOrder
// with anything it omits appended in registry order.
//
// Deriving is the point. The listing used to be authored alongside
// the registry as a second copy of the same facts, and a second copy
// drifts: `configure` advertised an `xrepo` that had no Command,
// `sparkwing --help` omitted the registered `examples` group, `run`
// hid its `config` child, and seventy synopses had been reworded on
// one side only. A reader cannot tell a stale listing from a true
// one, so the listing has to be incapable of being stale.
func filterSubcommands(parent Command, dropHideFromComplete bool) []SubcommandRef {
	kids := childCommands(parent.Path)
	byName := make(map[string]*Command, len(kids))
	for _, c := range kids {
		byName[commandLeafName(c.Path)] = c
	}

	out := make([]SubcommandRef, 0, len(kids))
	emitted := make(map[string]bool, len(kids))
	emit := func(c *Command) {
		name := commandLeafName(c.Path)
		if emitted[name] {
			return
		}
		emitted[name] = true
		// Hidden children are dispatchable but deliberately off the
		// offered surface -- listing one would put a "do not use this"
		// entry in the place a reader goes to choose what to use (the
		// same call runCommands makes). Deriving must not undo that.
		if c.Hidden {
			return
		}
		if dropHideFromComplete && c.HideFromComplete {
			return
		}
		out = append(out, SubcommandRef{Name: name, Synopsis: c.Synopsis})
	}

	for _, name := range parent.SubcommandOrder {
		if c, ok := byName[name]; ok {
			emit(c)
		}
	}
	for _, c := range kids {
		emit(c)
	}
	return out
}

// childCommands returns the Commands registered directly under path
// -- one argv word deeper, not the whole subtree -- in allCommands
// order.
func childCommands(path string) []*Command {
	prefix := path + " "
	var out []*Command
	for _, c := range allCommands {
		rest, ok := strings.CutPrefix(c.Path, prefix)
		if !ok || rest == "" || strings.Contains(rest, " ") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// commandLeafName is a command's final path word: the token a user
// types to reach it from its parent.
func commandLeafName(path string) string {
	if i := strings.LastIndex(path, " "); i >= 0 {
		return path[i+1:]
	}
	return path
}

func hasFlagNamed(flags []FlagSpec, name string) bool {
	for _, f := range flags {
		if f.Name == name {
			return true
		}
	}
	return false
}

// FlagValues holds typed pointers returned by bindFlags. Missing keys
// panic -- programmer error, not user error.
type FlagValues map[string]any

func (v FlagValues) String(name string) string {
	p, ok := v[name].(*string)
	if !ok {
		panic(fmt.Sprintf("FlagValues.String: %q not bound as string", name))
	}
	return *p
}

func (v FlagValues) Bool(name string) bool {
	p, ok := v[name].(*bool)
	if !ok {
		panic(fmt.Sprintf("FlagValues.Bool: %q not bound as bool", name))
	}
	return *p
}

func (v FlagValues) Int(name string) int {
	p, ok := v[name].(*int)
	if !ok {
		panic(fmt.Sprintf("FlagValues.Int: %q not bound as int", name))
	}
	return *p
}

func (v FlagValues) Int64(name string) int64 {
	p, ok := v[name].(*int64)
	if !ok {
		panic(fmt.Sprintf("FlagValues.Int64: %q not bound as int64", name))
	}
	return *p
}

func (v FlagValues) Duration(name string) time.Duration {
	p, ok := v[name].(*time.Duration)
	if !ok {
		panic(fmt.Sprintf("FlagValues.Duration: %q not bound as duration", name))
	}
	return *p
}

func (v FlagValues) StringSlice(name string) []string {
	p, ok := v[name].(*[]string)
	if !ok {
		panic(fmt.Sprintf("FlagValues.StringSlice: %q not bound as stringSlice", name))
	}
	return *p
}

// bindFlags registers each typed flag with fs. Untyped specs are skipped.
// Panics on misconfiguration (unknown FlagType, DefaultValue type mismatch).
func bindFlags(cmd Command, fs *flag.FlagSet) FlagValues {
	out := FlagValues{}
	for _, f := range cmd.Flags {
		if f.Type == "" {
			continue
		}
		switch f.Type {
		case FlagString:
			def := defaultAs[string](f, "")
			if f.Short != "" {
				out[f.Name] = fs.StringP(f.Name, f.Short, def, f.Desc)
			} else {
				out[f.Name] = fs.String(f.Name, def, f.Desc)
			}
		case FlagBool:
			def := defaultAs[bool](f, false)
			if f.Short != "" {
				out[f.Name] = fs.BoolP(f.Name, f.Short, def, f.Desc)
			} else {
				out[f.Name] = fs.Bool(f.Name, def, f.Desc)
			}
		case FlagInt:
			def := defaultAs[int](f, 0)
			if f.Short != "" {
				out[f.Name] = fs.IntP(f.Name, f.Short, def, f.Desc)
			} else {
				out[f.Name] = fs.Int(f.Name, def, f.Desc)
			}
		case FlagInt64:
			def := defaultAs[int64](f, 0)
			if f.Short != "" {
				out[f.Name] = fs.Int64P(f.Name, f.Short, def, f.Desc)
			} else {
				out[f.Name] = fs.Int64(f.Name, def, f.Desc)
			}
		case FlagDuration:
			var def time.Duration
			switch dv := f.DefaultValue.(type) {
			case nil:
			case time.Duration:
				def = dv
			case string:
				d, err := time.ParseDuration(dv)
				if err != nil {
					panic(fmt.Sprintf("bindFlags: --%s default %q: %v", f.Name, dv, err))
				}
				def = d
			default:
				panic(fmt.Sprintf("bindFlags: --%s DefaultValue must be time.Duration or string, got %T", f.Name, f.DefaultValue))
			}
			if f.Short != "" {
				out[f.Name] = fs.DurationP(f.Name, f.Short, def, f.Desc)
			} else {
				out[f.Name] = fs.Duration(f.Name, def, f.Desc)
			}
		case FlagStringSlice:
			def := defaultAs[[]string](f, nil)
			if f.Short != "" {
				out[f.Name] = fs.StringSliceP(f.Name, f.Short, def, f.Desc)
			} else {
				out[f.Name] = fs.StringSlice(f.Name, def, f.Desc)
			}
		default:
			panic(fmt.Sprintf("bindFlags: --%s unknown FlagType %q", f.Name, f.Type))
		}
	}
	return out
}

func defaultAs[T any](f FlagSpec, fallback T) T {
	if f.DefaultValue == nil {
		return fallback
	}
	v, ok := f.DefaultValue.(T)
	if !ok {
		panic(fmt.Sprintf("bindFlags: --%s DefaultValue type mismatch (got %T)", f.Name, f.DefaultValue))
	}
	return v
}

// handleParentHelp matches help only as the first arg so subcommands
// keep their own --help (`tokens create --help` routes to that handler).
func handleParentHelp(cmd Command, args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "--help", "help":
		renderHelp(cmd, args, os.Stdout)
		return true
	}
	return false
}

// renderHelp picks JSON vs prose based on raw args (FlagSet may not
// have --json/--output declared by the time we get here).
func renderHelp(cmd Command, args []string, w io.Writer) {
	if wantsJSONHelp(args) {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(toCommandJSON(&cmd))
		return
	}
	PrintHelp(cmd, w)
}

// visibleFlagsForHelp returns the flag set rendered for a help/completion
// surface. `hotOnly=true` filters to Hot flags only (tab-completion
// uses this for a curated menu); `hotOnly=false` returns every non-
// Hidden flag for the full --help output. The helpFlag is always
// appended when missing.
func visibleFlagsForHelp(cmd Command, hotOnly bool) []FlagSpec {
	hasHot := false
	if hotOnly {
		for _, f := range cmd.Flags {
			if f.Hot {
				hasHot = true
				break
			}
		}
	}
	var out []FlagSpec
	for _, f := range cmd.Flags {
		if f.Hidden {
			continue
		}
		if hotOnly && hasHot && !f.Hot {
			continue
		}
		out = append(out, f)
	}
	if !hasFlagNamed(out, "help") {
		out = append(out, helpFlag)
	}
	return out
}

func wantsJSONHelp(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			return true
		case a == "--output=json", a == "-o=json":
			return true
		case a == "--output", a == "-o":
			if i+1 < len(args) && args[i+1] == "json" {
				return true
			}
		}
	}
	return false
}
