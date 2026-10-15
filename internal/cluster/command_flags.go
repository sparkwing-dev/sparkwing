package cluster

import (
	"errors"
	"fmt"
	"os"
	"slices"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

type usageError struct{ error }

func commandFlags(command string, args []string) (_ *flag.FlagSet, parseErr error) {
	defer func() {
		if parseErr != nil && !errors.Is(parseErr, flag.ErrHelp) {
			parseErr = &usageError{parseErr}
		}
	}()
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
	fs.SetOutput(os.Stderr)
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
	if enabled(fs, "help") || enabled(fs, "help-all") {
		fmt.Fprintf(os.Stderr, "usage: sparkwing-runner %s [flags]\n", command)
		fs.PrintDefaults()
		return nil, flag.ErrHelp
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	for _, definition := range definitions {
		if definition.Required && (!fs.Changed(definition.Name) || textFlag(fs, definition.Name) == "") {
			return nil, fmt.Errorf("--%s is required", definition.Name)
		}
		if fs.Changed(definition.Name) && len(definition.Values) > 0 && !slices.Contains(definition.Values, textFlag(fs, definition.Name)) {
			return nil, fmt.Errorf("invalid --%s", definition.Name)
		}
	}
	return fs, nil
}

func textFlag(fs *flag.FlagSet, name string) string { value, _ := fs.GetString(name); return value }
func enabled(fs *flag.FlagSet, name string) bool    { value, _ := fs.GetBool(name); return value }

func parsedHelp(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}
