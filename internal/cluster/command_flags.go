package cluster

import (
	"errors"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
)

type usageError struct{ error }

func commandFlags(command string, args []string) (_ *flag.FlagSet, parseErr error) {
	defer func() {
		if parseErr != nil && !errors.Is(parseErr, flag.ErrHelp) {
			parseErr = &usageError{parseErr}
		}
	}()
	return runnerflags.Parse(command, args, os.Stderr)
}

func textFlag(fs *flag.FlagSet, name string) string { value, _ := fs.GetString(name); return value }
func enabled(fs *flag.FlagSet, name string) bool    { value, _ := fs.GetBool(name); return value }

func parsedHelp(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}
