package egress

import (
	"fmt"
	"strconv"
	"strings"

	flag "github.com/spf13/pflag"
)

// Environment fallbacks for the egress budget flags. A deployment sets
// budgets through the environment; the flags override them.
const (
	EnvMonthlyBytes    = "SPARKWING_EGRESS_MONTHLY_BYTES"
	EnvDailyAlarmBytes = "SPARKWING_EGRESS_DAILY_ALARM_BYTES"
	EnvMaxLogStreams   = "SPARKWING_EGRESS_MAX_LOG_STREAMS"
)

// Flag names, so a refusal can name the flag that raises the budget it
// refused against.
const (
	FlagMonthlyBytes    = "--egress-monthly-bytes"
	FlagDailyAlarmBytes = "--egress-daily-alarm-bytes"
	FlagMaxLogStreams   = "--egress-max-log-streams"
)

// WithLogStreams and WithoutLogStreams name [Bind]'s third argument at
// the call site. A service that holds no live log stream open declares
// no cap on one.
const (
	WithLogStreams    = true
	WithoutLogStreams = false
)

// Bind registers the egress budget flags on fs and returns the reader
// that turns the parsed values into a [Config]. Every budget defaults to
// its environment fallback, and to unlimited when that is unset, so a
// service given no budget serves exactly what it served before.
//
// The reader reports a malformed environment fallback and a negative
// flag value rather than serving a budget the operator did not mean.
func Bind(fs *flag.FlagSet, getenv func(string) string, logStreams bool) func() (Config, error) {
	monthly, monthlyErr := int64Env(getenv, EnvMonthlyBytes)
	daily, dailyErr := int64Env(getenv, EnvDailyAlarmBytes)
	streams, streamsErr := int64Env(getenv, EnvMaxLogStreams)

	monthlyFlag := fs.Int64("egress-monthly-bytes", monthly,
		"bytes one principal may download in a UTC month across artifacts, logs, and git "+
			"fetches, after which its downloads are refused with 429 until the month rolls. "+
			"0, the default, is unlimited (env: "+EnvMonthlyBytes+")")
	dailyFlag := fs.Int64("egress-daily-alarm-bytes", daily,
		"bytes this process may send in a UTC day before it raises the egress alarm, which "+
			"the health route reports and the log carries at warn level. It refuses nothing. "+
			"0, the default, disables the alarm (env: "+EnvDailyAlarmBytes+")")
	var streamsFlag *int64
	if logStreams {
		streamsFlag = fs.Int64("egress-max-log-streams", streams,
			"live log streams one principal may hold open at once; a further one is refused "+
				"with 429. 0, the default, is unlimited (env: "+EnvMaxLogStreams+")")
	}

	return func() (Config, error) {
		for _, err := range []error{monthlyErr, dailyErr, streamsErr} {
			if err != nil {
				return Config{}, err
			}
		}
		cfg := Config{PerPrincipalMonthlyBytes: *monthlyFlag, GlobalDailyAlarmBytes: *dailyFlag}
		if streamsFlag != nil {
			cfg.MaxStreamsPerPrincipal = int(*streamsFlag)
		}
		return cfg, cfg.Validate()
	}
}

// Validate reports a budget spelled with a negative number, which is
// neither a limit nor "unlimited" and so is refused rather than guessed.
func (c Config) Validate() error {
	for _, b := range []struct {
		flag  string
		value int64
	}{
		{FlagMonthlyBytes, c.PerPrincipalMonthlyBytes},
		{FlagDailyAlarmBytes, c.GlobalDailyAlarmBytes},
		{FlagMaxLogStreams, int64(c.MaxStreamsPerPrincipal)},
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
