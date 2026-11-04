// Package runnerflags parses the shared runner grammar for commands and service inspection.
package runnerflags

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

// Parse reads declared flags without configuration or credentials. Help goes
// to stdout in the selected output mode, as the sparkwing CLI writes it, and
// parse failures go to diagnostics.
func Parse(command string, args []string, stdout, diagnostics io.Writer) (*flag.FlagSet, error) {
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
	for _, definition := range definitions {
		if !fs.Changed(definition.Name) || len(definition.Values) == 0 {
			continue
		}
		value, err := fs.GetString(definition.Name)
		if err != nil || !slices.Contains(definition.Values, value) {
			return nil, fmt.Errorf("invalid --%s", definition.Name)
		}
	}
	help, _ := fs.GetBool("help")
	helpAll, _ := fs.GetBool("help-all")
	if help || helpAll {
		var text bytes.Buffer
		fmt.Fprintf(&text, "usage: sparkwing-runner %s [flags]\n", command)
		fs.SetOutput(&text)
		fs.PrintDefaults()
		return nil, errors.Join(flag.ErrHelp, writeHelp(stdout, fs, text.String()))
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	for _, definition := range definitions {
		value, _ := fs.GetString(definition.Name)
		if definition.Required && (!fs.Changed(definition.Name) || value == "") {
			return nil, fmt.Errorf("--%s is required", definition.Name)
		}
	}
	return fs, nil
}

func writeHelp(stdout io.Writer, fs *flag.FlagSet, text string) error {
	output, err := fs.GetString("output")
	if err != nil {
		return err
	}
	if output == "" && !color.IsInteractiveStdout() {
		output = "json"
	}
	if output == "json" {
		return json.NewEncoder(stdout).Encode(struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}{Kind: "help", Text: text})
	}
	_, err = io.WriteString(stdout, text)
	return err
}
