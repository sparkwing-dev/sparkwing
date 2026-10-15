// Package runnerflags parses the shared runner grammar for commands and service inspection.
package runnerflags

import (
	"fmt"
	"io"
	"slices"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

// Parse reads declared flags without configuration or credentials and writes usage to diagnostics.
func Parse(command string, args []string, diagnostics io.Writer) (*flag.FlagSet, error) {
	var spec *clisurface.Command
	for _, candidate := range clisurface.Registry() {
		if candidate.Path == "sparkwing-runner "+command {
			spec = &candidate
			break
		}
	}
	if spec == nil {
		return nil, fmt.Errorf("unknown subcommand %q", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	definitions := append(clisurface.GlobalFlags(), spec.Flags...)
	for _, definition := range definitions {
		switch {
		case definition.Argument == "":
			fs.BoolP(definition.Name, definition.Short, false, definition.Meaning)
		case definition.Repeatable:
			fs.StringSliceP(definition.Name, definition.Short, nil, definition.Argument)
		default:
			fs.StringP(definition.Name, definition.Short, "", definition.Argument)
		}
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	help, _ := fs.GetBool("help")
	helpAll, _ := fs.GetBool("help-all")
	if help || helpAll {
		fmt.Fprintf(diagnostics, "usage: sparkwing-runner %s [flags]\n", command)
		fs.PrintDefaults()
		return nil, flag.ErrHelp
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	for _, definition := range definitions {
		value, _ := fs.GetString(definition.Name)
		if definition.Required && (!fs.Changed(definition.Name) || value == "") {
			return nil, fmt.Errorf("--%s is required", definition.Name)
		}
		if fs.Changed(definition.Name) && len(definition.Values) > 0 && !slices.Contains(definition.Values, value) {
			return nil, fmt.Errorf("invalid --%s", definition.Name)
		}
	}
	return fs, nil
}
