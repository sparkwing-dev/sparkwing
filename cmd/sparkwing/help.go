package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type FlagType string

const (
	FlagString      FlagType = "string"
	FlagBool        FlagType = "bool"
	FlagInt         FlagType = "int"
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

	Type FlagType

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

	// ReachesController marks a verb that can act on a controller or a
	// profile's remote backends. It reaches one only when --profile names
	// it on that invocation, so every such verb must accept --profile.
	ReachesController bool
}

var helpFlag = FlagSpec{
	Name:  "help",
	Short: "h",
	Desc:  "Show help for this command (including hidden flags) and exit",
	Group: "Other",
	Hot:   true,
}

var errHelpRequested = errors.New("help requested")

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

func writeCommandHelp(cmd Command, w io.Writer, mode string) error {
	var text bytes.Buffer
	PrintHelp(cmd, &text)
	return writeHelpText(cmd, w, mode, text.String())
}

func writeHelpText(cmd Command, w io.Writer, mode, text string) error {
	if mode == "json" {
		return json.NewEncoder(w).Encode(struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
			CommandJSON
		}{Kind: "help", Text: text, CommandJSON: toCommandJSON(&cmd)})
	}
	_, err := io.WriteString(w, text)
	return err
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
