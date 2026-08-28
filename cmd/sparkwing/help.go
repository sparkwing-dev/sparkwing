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

type FlagType string

const (
	FlagString      FlagType = "string"
	FlagBool        FlagType = "bool"
	FlagInt         FlagType = "int"
	FlagInt64       FlagType = "int64"
	FlagDuration    FlagType = "duration"
	FlagStringSlice FlagType = "stringSlice"
)

type FlagSpec struct {
	Name     string
	Short    string
	Argument string
	Desc     string
	Group    string

	Required      bool
	RequiredWhen  string
	RequiresFlags []string
	ConflictsWith []string

	Default string

	Type         FlagType
	DefaultValue any

	Hidden bool

	Hot bool
}

type PosArg struct {
	Name     string
	Desc     string
	Required bool
}

type Example struct {
	Desc    string
	Command string
}

type SubcommandRef struct {
	Name     string
	Synopsis string
}

type Command struct {
	Path        string
	Synopsis    string
	Description string

	SubcommandOrder    []string
	SubcommandOptional bool

	PosArgs     []PosArg
	Flags       []FlagSpec
	Examples    []Example
	GroupOrder  []string
	UsageSuffix string

	Hidden bool

	HideFromComplete bool
}

var helpFlag = FlagSpec{
	Name:  "help",
	Short: "h",
	Desc:  "Show help for this command (including hidden flags) and exit",
	Group: "Other",
	Hot:   true,
}

var errHelpRequested = errors.New("help requested")

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

	visible := visibleSubcommands(cmd)

	if cmd.Synopsis != "" {
		fmt.Fprintln(w, cmd.Synopsis)
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "USAGE")
	fmt.Fprint(w, "  ", cmd.Path)
	for _, a := range cmd.PosArgs {
		name := a.Name
		if !a.Required && !(strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]")) {
			name = "[" + name + "]"
		}
		fmt.Fprint(w, " ", name)
	}
	if len(visible) > 0 {
		if cmd.SubcommandOptional {
			fmt.Fprint(w, " [<subcommand>]")
		} else {
			fmt.Fprint(w, " <subcommand>")
		}
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

func completableSubcommands(parent Command) []SubcommandRef {
	return filterSubcommands(parent, true)
}

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

func renderHelp(cmd Command, args []string, w io.Writer) {
	if wantsJSONHelp(args) {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(toCommandJSON(&cmd))
		return
	}
	PrintHelp(cmd, w)
}

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
