package egress

import (
	"fmt"
	"strings"

	flag "github.com/spf13/pflag"
)

// FlagNames are the flags one service spells its budgets with, so a
// refusal names the flag that raises the budget it refused against.
type FlagNames struct {
	DailyAlarmBytes string
	MaxLogStreams   string
	MaxDownloads    string
}

// Unprefixed flag names, which are also what a refusal names when a
// Config carries no FlagNames of its own.
const (
	flagDailyAlarmBytes = "--egress-daily-alarm-bytes"
	flagMaxLogStreams   = "--egress-max-log-streams"
	flagMaxDownloads    = "--egress-max-downloads"
)

func (f FlagNames) orDefault() FlagNames {
	if f.DailyAlarmBytes == "" {
		f.DailyAlarmBytes = flagDailyAlarmBytes
	}
	if f.MaxLogStreams == "" {
		f.MaxLogStreams = flagMaxLogStreams
	}
	if f.MaxDownloads == "" {
		f.MaxDownloads = flagMaxDownloads
	}
	return f
}

// Named reports which budgets the operator set themselves on the command line. A caller filling unset budgets from a profile needs it
// to tell a budget nobody named from one deliberately set to zero, which is
// unlimited and a value in its own right.
type Named struct {
	DailyAlarmBytes bool
	MaxLogStreams   bool
	MaxDownloads    bool
}

// Bind registers the controller's egress flags and returns their validated configuration.
func Bind(fs *flag.FlagSet) func() (Config, Named, error) {
	names := FlagNames{
		DailyAlarmBytes: flagDailyAlarmBytes,
		MaxLogStreams:   flagMaxLogStreams,
		MaxDownloads:    flagMaxDownloads,
	}
	dailyFlag := fs.Int64("egress-daily-alarm-bytes", 0,
		"bytes this process may send in a UTC day before it raises the egress alarm, which "+
			"the health route and the metrics report and the log carries at warn level. It refuses "+
			"nothing. 0, the default, disables the alarm")

	downloadsFlag := fs.Int64("egress-max-downloads", 0,
		"metered downloads one principal may hold open at once; a further one is refused "+
			"with 429. Every pod sharing a bearer counts against the same cap. 0, the "+
			"default, is unlimited")
	streamsFlag := fs.Int64("egress-max-log-streams", 0,
		"live log streams one principal may hold open at once; a further one is refused "+
			"with 429. 0, the default, is unlimited")

	return func() (Config, Named, error) {
		named := Named{
			DailyAlarmBytes: fs.Changed(trimFlag(names.DailyAlarmBytes)),
			MaxLogStreams:   fs.Changed(trimFlag(names.MaxLogStreams)),
			MaxDownloads:    fs.Changed(trimFlag(names.MaxDownloads)),
		}
		cfg := Config{
			GlobalDailyAlarmBytes: *dailyFlag, Flags: names,
			MaxDownloadsPerPrincipal: int(*downloadsFlag),
			MaxStreamsPerPrincipal:   int(*streamsFlag),
		}
		return cfg, named, cfg.validate()
	}
}

// safety: the flag names carry their leading dashes so a refusal can print
// them, and a flag set is asked without.
func trimFlag(name string) string {
	return strings.TrimPrefix(name, "--")
}

// validate reports a budget spelled with a negative number, which is
// neither a limit nor "unlimited" and so is refused rather than guessed.
func (c Config) validate() error {
	names := c.Flags.orDefault()
	for _, b := range []struct {
		flag  string
		value int64
	}{
		{names.DailyAlarmBytes, c.GlobalDailyAlarmBytes},
		{names.MaxLogStreams, int64(c.MaxStreamsPerPrincipal)},
		{names.MaxDownloads, int64(c.MaxDownloadsPerPrincipal)},
	} {
		if b.value < 0 {
			return fmt.Errorf("%s must not be negative; 0 is unlimited", b.flag)
		}
	}
	return nil
}
