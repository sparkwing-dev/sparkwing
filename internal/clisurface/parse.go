package clisurface

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Invocation retains explicit flag presence separately from values and defaults.
// Args contains only the pipeline arguments following --.
type Invocation struct {
	Command  Command
	Flags    map[string][]string
	Pipeline string
	Args     []string
}

// Value returns the scalar value of a flag, or empty when it was not supplied.
func (i Invocation) Value(name string) string {
	values := i.Flags[name]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// Has distinguishes an explicit false, zero or empty value from an absent flag.
func (i Invocation) Has(name string) bool { _, ok := i.Flags[name]; return ok }

// Bool returns the parsed boolean value, defaulting to false for an absent flag.
func (i Invocation) Bool(name string) bool { return i.Value(name) == "true" }

// Help reports whether execution should be replaced by command help.
func (i Invocation) Help() bool { return i.Bool("help") || i.Bool("help-all") }

// Output selects the format before any command side effects.
func (i Invocation) Output(terminal bool) string {
	if value := i.Value("output"); value != "" {
		return value
	}
	if terminal {
		return "pretty"
	}
	return "json"
}

// UsageError identifies a rejected invocation without echoing argument values.
type UsageError struct {
	Path    string
	Message string
}

func (e *UsageError) Error() string {
	binary, command, _ := strings.Cut(e.Path, " ")
	if command == "" {
		return binary + ": " + e.Message
	}
	return binary + ": " + command + ": " + e.Message
}

// exitCode separates parser failures from command failures.
func (e *UsageError) exitCode() ExitCode { return ExitUsage }

func (i Invocation) invalid(format string, args ...any) error {
	return &UsageError{Path: i.Command.Path, Message: fmt.Sprintf(format, args...)}
}

// Parse validates the final command registry without consulting files, environment
// or credentials. Flags may precede the command; their legality belongs to the
// selected leaf. Pipeline arguments are opaque after --.
func Parse(binary string, args []string) (Invocation, error) {
	commands := Registry()
	inv := Invocation{Flags: make(map[string][]string)}
	paths := make(map[string]Command, len(commands))
	flags, shorts := map[string]Flag{}, map[string]string{}
	for _, c := range commands {
		paths[c.Path] = c
		if c.Path != binary && !strings.HasPrefix(c.Path, binary+" ") {
			continue
		}
		for _, f := range append(GlobalFlags(), c.Flags...) {
			flags[f.Name] = f
			if f.Short != "" {
				shorts[f.Short] = f.Name
			}
		}
	}
	root, ok := paths[binary]
	if !ok {
		return inv, &UsageError{Path: binary, Message: "unknown executable"}
	}
	inv.Command = root
	separator := false
	for n := 0; n < len(args); n++ {
		arg := args[n]
		if arg == "--" {
			separator, inv.Args = true, slices.Clone(args[n+1:])
			break
		}
		if strings.HasPrefix(arg, "-") {
			name, value, explicit, err := flagToken(arg, shorts)
			if err != nil {
				return inv, inv.invalid("%s", err)
			}
			f, known := flags[name]
			if !known {
				return inv, inv.invalid("unknown flag --%s%s", name, inv.flagHint())
			}
			if f.Argument != "" && !explicit {
				if n+1 >= len(args) || args[n+1] == "--" || strings.HasPrefix(args[n+1], "--") {
					return inv, inv.invalid("--%s requires %s", name, f.Argument)
				}
				n++
				value = args[n]
			} else if f.Argument == "" && !explicit {
				value = "true"
			}
			inv.Flags[name] = append(inv.Flags[name], value)
			continue
		}
		if child, found := paths[inv.Command.Path+" "+arg]; found && inv.Pipeline == "" {
			inv.Command = child
		} else if inv.Command.PipelineArgument && inv.Pipeline == "" && arg != "" {
			inv.Pipeline = arg
		} else {
			return inv, inv.invalid("unexpected positional argument; use a kind-named flag")
		}
	}
	if separator && !inv.Command.PipelineArgsAfterSeparator {
		return inv, inv.invalid("pipeline arguments are not accepted by this command")
	}
	if err := inv.validate(); err != nil {
		return inv, err
	}
	return inv, nil
}

// safety: only a command that forwards pipeline arguments points at --; any
// other names its own flags, so the hint never sends a user to a separator.
func (i Invocation) flagHint() string {
	if i.Command.PipelineArgsAfterSeparator {
		return "; pipeline arguments follow --"
	}
	var names []string
	for _, f := range i.Command.Flags {
		names = append(names, "--"+f.Name)
	}
	if len(names) == 0 {
		return "; see " + i.Command.Path + " --help"
	}
	return "; " + i.Command.Path + " takes " + strings.Join(names, ", ") + " (see --help)"
}

func flagToken(arg string, shorts map[string]string) (name, value string, explicit bool, err error) {
	if strings.HasPrefix(arg, "--") {
		name, value, explicit = strings.Cut(arg[2:], "=")
		return name, value, explicit, err
	}
	if len(arg) < 2 {
		return "", "", false, fmt.Errorf("unexpected positional argument")
	}
	name, ok := shorts[arg[1:2]]
	if !ok {
		return "", "", false, fmt.Errorf("unknown short flag -%s", arg[1:2])
	}
	if len(arg) > 2 {
		return name, strings.TrimPrefix(arg[2:], "="), true, nil
	}
	return name, "", false, nil
}

func (i *Invocation) validate() error {
	definitions := append(GlobalFlags(), i.Command.Flags...)
	allowed := make(map[string]Flag, len(definitions))
	for _, f := range definitions {
		allowed[f.Name] = f
	}
	// safety: sorted diagnostics are stable even when several flags are invalid.
	names := make([]string, 0, len(i.Flags))
	for name := range i.Flags {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f, ok := allowed[name]
		if !ok {
			if i.Command.Path == "sparkwing run" && name == "profile" {
				return i.invalid("--profile is not accepted by local run; use sparkwing pipeline trigger <pipeline> --profile NAME")
			}
			return i.invalid("flag --%s is not accepted%s", name, i.flagHint())
		}
		if !f.Repeatable && len(i.Flags[name]) > 1 {
			return i.invalid("--%s may be supplied only once", name)
		}
		var values []string
		for _, raw := range i.Flags[name] {
			parts := []string{raw}
			if f.CommaList {
				parts = strings.Split(raw, ",")
			}
			for _, value := range parts {
				if err := validateFlag(f, value); err != nil {
					return i.invalid("--%s %s", name, err)
				}
				values = append(values, value)
			}
		}
		i.Flags[name] = values
	}
	if i.Help() {
		return nil
	}
	for _, f := range definitions {
		if f.Required && !i.Has(f.Name) {
			if f.Meaning != "" {
				return i.invalid("--%s is required; %s", f.Name, f.Meaning)
			}
			return i.invalid("--%s is required", f.Name)
		}
		if !i.Has(f.Name) || f.Argument == "" && !i.Bool(f.Name) {
			continue
		}
		for _, required := range f.Requires {
			if !i.Has(required) || allowed[required].Argument == "" && !i.Bool(required) {
				return i.invalid("--%s requires --%s", f.Name, required)
			}
		}
		for _, conflict := range f.Conflicts {
			other := allowed[conflict]
			if i.Has(conflict) && (other.Argument != "" || i.Bool(conflict)) {
				return i.invalid("--%s conflicts with --%s", f.Name, conflict)
			}
		}
	}
	if i.Command.PipelineArgument && i.Pipeline == "" && !(i.Command.PipelineOptionalOnResume && i.Has("resume")) {
		return i.invalid("pipeline name is required")
	}
	if i.Command.Path == "sparkwing pipeline trigger" && i.Has("resume") {
		if i.Pipeline != "" || len(i.Args) != 0 {
			return i.invalid("--resume uses the sealed pipeline and arguments")
		}
		if i.Has("runner") && i.Value("runner") != "here" {
			return i.invalid("--resume requires --runner here or no --runner")
		}
		for _, name := range []string{"reason", "ref", "pipeline-ref", "working-tree", "allow-secret-file", "detach"} {
			if i.Has(name) {
				return i.invalid("--resume cannot be combined with --%s", name)
			}
		}
	}
	return nil
}

func validateFlag(f Flag, value string) error {
	if f.Argument == "" {
		if value != "true" && value != "false" {
			return fmt.Errorf("requires true or false")
		}
		return nil
	}
	if value == "" && !f.AllowEmpty && f.Name != "arg" && (f.CommaList || f.Argument != "VALUE" && f.Name != "tz") {
		return fmt.Errorf("requires %s", f.Argument)
	}
	if len(f.Values) > 0 && !slices.Contains(f.Values, value) {
		return fmt.Errorf("requires one of %s", strings.Join(f.Values, ", "))
	}
	if f.Argument == "N" || f.Argument == "BYTES" || f.HasMinimum {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("requires an integer")
		}
		if f.HasMinimum && n < f.Minimum {
			return fmt.Errorf("must be at least %d", f.Minimum)
		}
	}
	if f.Argument == "DURATION" || f.Argument == "DUR" {
		parse := time.ParseDuration
		if f.Name == "since" || f.Name == "older-than" {
			parse = ParseLookback
		}
		if _, err := parse(value); err != nil {
			return fmt.Errorf("requires a duration")
		}
	}
	return nil
}
