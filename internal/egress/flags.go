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
	FlagDailyAlarmBytes = "--egress-daily-alarm-bytes"
	FlagMaxLogStreams   = "--egress-max-log-streams"
	FlagMaxDownloads    = "--egress-max-downloads"
)

func (f FlagNames) orDefault() FlagNames {
	if f.DailyAlarmBytes == "" {
		f.DailyAlarmBytes = FlagDailyAlarmBytes
	}
	if f.MaxLogStreams == "" {
		f.MaxLogStreams = FlagMaxLogStreams
	}
	if f.MaxDownloads == "" {
		f.MaxDownloads = FlagMaxDownloads
	}
	return f
}

// Surfaces says which budgets a service can enforce, so a service that
// holds no live log stream open and one that cannot tell its callers
// apart each register only the flags that mean something to them.
type Surfaces struct {
	// PerPrincipal registers the download concurrency cap. A service
	// whose callers all resolve to one principal leaves it false: its
	// refusal would fall on the whole fleet at once.
	PerPrincipal bool
	// LogStreams registers the live log stream cap.
	LogStreams bool
}

// ControllerSurfaces, LogsSurfaces and CacheSurfaces name what each
// binary passes to [Bind].
var (
	ControllerSurfaces = Surfaces{PerPrincipal: true, LogStreams: true}
	LogsSurfaces       = Surfaces{PerPrincipal: true, LogStreams: true}
	CacheSurfaces      = Surfaces{}
)

// Named reports which budgets the operator set themselves on the command line. A caller filling unset budgets from a profile needs it
// to tell a budget nobody named from one deliberately set to zero, which is
// unlimited and a value in its own right.
type Named struct {
	DailyAlarmBytes bool
	MaxLogStreams   bool
	MaxDownloads    bool
}

// Bind registers a service's egress budget flags on fs and returns the reader
// that turns the parsed values into a [Config]. Every budget defaults to
// unlimited, so a service given no budget serves exactly what it served
// before. The reader refuses a negative value rather than serving a budget
// the operator did not mean.
func Bind(fs *flag.FlagSet, surfaces Surfaces) func() (Config, Named, error) {
	names := FlagNames{
		DailyAlarmBytes: FlagDailyAlarmBytes,
		MaxLogStreams:   FlagMaxLogStreams,
		MaxDownloads:    FlagMaxDownloads,
	}
	dailyFlag := fs.Int64("egress-daily-alarm-bytes", 0,
		"bytes this process may send in a UTC day before it raises the egress alarm, which "+
			"the health route and the metrics report and the log carries at warn level. It refuses "+
			"nothing. 0, the default, disables the alarm")

	var downloadsFlag, streamsFlag *int64
	if surfaces.PerPrincipal {
		downloadsFlag = fs.Int64("egress-max-downloads", 0,
			"metered downloads one principal may hold open at once; a further one is refused "+
				"with 429. Every pod sharing a bearer counts against the same cap. 0, the "+
				"default, is unlimited")
	}
	if surfaces.LogStreams {
		streamsFlag = fs.Int64("egress-max-log-streams", 0,
			"live log streams one principal may hold open at once; a further one is refused "+
				"with 429. 0, the default, is unlimited")
	}

	return func() (Config, Named, error) {
		named := Named{
			DailyAlarmBytes: fs.Changed(trimFlag(names.DailyAlarmBytes)),
			MaxLogStreams:   fs.Changed(trimFlag(names.MaxLogStreams)),
			MaxDownloads:    fs.Changed(trimFlag(names.MaxDownloads)),
		}
		cfg := Config{GlobalDailyAlarmBytes: *dailyFlag, Flags: names}
		if downloadsFlag != nil {
			cfg.MaxDownloadsPerPrincipal = int(*downloadsFlag)
		}
		if streamsFlag != nil {
			cfg.MaxStreamsPerPrincipal = int(*streamsFlag)
		}
		return cfg, named, cfg.Validate()
	}
}

// safety: the flag names carry their leading dashes so a refusal can print
// them, and a flag set is asked without.
func trimFlag(name string) string {
	return strings.TrimPrefix(name, "--")
}

// Validate reports a budget spelled with a negative number, which is
// neither a limit nor "unlimited" and so is refused rather than guessed.
func (c Config) Validate() error {
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
