package egress

import (
	"fmt"
	"strconv"
	"strings"

	flag "github.com/spf13/pflag"
)

// Service names the binary a budget belongs to. Each service reads its
// own environment variables, because one variable read by three
// processes on a shared ConfigMap is one cap applied three times, not
// one cap: a fleet under a 100 GiB month would be admitted 300 GiB.
type Service string

const (
	// ServiceController budgets sparkwing-controller, the one service
	// that resolves a bearer to a named principal on every download
	// route, and so the one that carries the per-team monthly cap.
	ServiceController Service = "CONTROLLER"
	// ServiceLogs budgets sparkwing-logs, which resolves principals
	// through the controller's whoami.
	ServiceLogs Service = "LOGS"
	// ServiceCache budgets sparkwing-cache, which authenticates one
	// shared token and so meters and alarms without refusing.
	ServiceCache Service = "CACHE"
)

// FlagNames are the flags one service spells its budgets with, so a
// refusal names the flag that raises the budget it refused against.
type FlagNames struct {
	MonthlyBytes    string
	DailyAlarmBytes string
	MaxLogStreams   string
	MaxDownloads    string
}

// Unprefixed flag names, which are also what a refusal names when a
// Config carries no FlagNames of its own.
const (
	FlagMonthlyBytes    = "--egress-monthly-bytes"
	FlagDailyAlarmBytes = "--egress-daily-alarm-bytes"
	FlagMaxLogStreams   = "--egress-max-log-streams"
	FlagMaxDownloads    = "--egress-max-downloads"
)

func (f FlagNames) orDefault() FlagNames {
	if f.MonthlyBytes == "" {
		f.MonthlyBytes = FlagMonthlyBytes
	}
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
	// PerPrincipal registers the monthly byte cap, the download
	// concurrency cap and, with LogStreams, the stream cap. A service
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

// EnvName returns the environment variable one service reads for one
// budget, for example SPARKWING_CONTROLLER_EGRESS_MONTHLY_BYTES.
func EnvName(svc Service, suffix string) string {
	return "SPARKWING_" + string(svc) + "_EGRESS_" + suffix
}

// Environment variable suffixes, appended after the service segment.
const (
	EnvMonthlyBytes    = "MONTHLY_BYTES"
	EnvDailyAlarmBytes = "DAILY_ALARM_BYTES"
	EnvMaxLogStreams   = "MAX_LOG_STREAMS"
	EnvMaxDownloads    = "MAX_DOWNLOADS"
)

// Bind registers svc's egress budget flags on fs and returns the reader
// that turns the parsed values into a [Config]. Every budget defaults to
// its own environment fallback, and to unlimited when that is unset, so
// a service given no budget serves exactly what it served before.
//
// The reader reports a malformed environment fallback and a negative
// flag value rather than serving a budget the operator did not mean.
func Bind(fs *flag.FlagSet, getenv func(string) string, svc Service, surfaces Surfaces) func() (Config, error) {
	names := FlagNames{
		MonthlyBytes:    FlagMonthlyBytes,
		DailyAlarmBytes: FlagDailyAlarmBytes,
		MaxLogStreams:   FlagMaxLogStreams,
		MaxDownloads:    FlagMaxDownloads,
	}
	var errs []error
	read := func(suffix string) *int64 {
		v, err := int64Env(getenv, EnvName(svc, suffix))
		if err != nil {
			errs = append(errs, err)
		}
		return &v
	}

	daily := read(EnvDailyAlarmBytes)
	dailyFlag := fs.Int64("egress-daily-alarm-bytes", *daily,
		"bytes this process may send in a UTC day before it raises the egress alarm, which "+
			"the health route reports and the log carries at warn level. It refuses nothing. "+
			"0, the default, disables the alarm (env: "+EnvName(svc, EnvDailyAlarmBytes)+")")

	var monthlyFlag, downloadsFlag, streamsFlag *int64
	if surfaces.PerPrincipal {
		monthly := read(EnvMonthlyBytes)
		monthlyFlag = fs.Int64("egress-monthly-bytes", *monthly,
			"bytes one principal may download in a UTC month across artifacts, logs, and git "+
				"fetches, after which its downloads are refused with 429 until the month rolls. "+
				"0, the default, is unlimited (env: "+EnvName(svc, EnvMonthlyBytes)+")")
		downloads := read(EnvMaxDownloads)
		downloadsFlag = fs.Int64("egress-max-downloads", *downloads,
			"metered downloads one principal may hold open at once; a further one is refused "+
				"with 429. It bounds how far a burst carries a principal past the monthly "+
				"budget, to this many times the largest object. 0, the default, is unlimited "+
				"(env: "+EnvName(svc, EnvMaxDownloads)+")")
	}
	if surfaces.LogStreams {
		streams := read(EnvMaxLogStreams)
		streamsFlag = fs.Int64("egress-max-log-streams", *streams,
			"live log streams one principal may hold open at once; a further one is refused "+
				"with 429. 0, the default, is unlimited (env: "+EnvName(svc, EnvMaxLogStreams)+")")
	}

	return func() (Config, error) {
		if len(errs) > 0 {
			return Config{}, errs[0]
		}
		cfg := Config{GlobalDailyAlarmBytes: *dailyFlag, Flags: names}
		if monthlyFlag != nil {
			cfg.PerPrincipalMonthlyBytes = *monthlyFlag
		}
		if downloadsFlag != nil {
			cfg.MaxDownloadsPerPrincipal = int(*downloadsFlag)
		}
		if streamsFlag != nil {
			cfg.MaxStreamsPerPrincipal = int(*streamsFlag)
		}
		return cfg, cfg.Validate()
	}
}

// Validate reports a budget spelled with a negative number, which is
// neither a limit nor "unlimited" and so is refused rather than guessed.
func (c Config) Validate() error {
	names := c.Flags.orDefault()
	for _, b := range []struct {
		flag  string
		value int64
	}{
		{names.MonthlyBytes, c.PerPrincipalMonthlyBytes},
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

func int64Env(getenv func(string) string, name string) (int64, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s=%q: want a whole non-negative number, or 0 for no limit", name, raw)
	}
	return n, nil
}
