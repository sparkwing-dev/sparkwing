package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type machineSettings struct {
	BoxID string `yaml:"box_id"`
}

type logsSettings struct {
	DropPolicy string `yaml:"drop_policy"`
}

type debugSettings struct {
	PauseTimeout string `yaml:"pause_timeout"`
}

type runSettings struct {
	SubmitEnvAllow []string `yaml:"submit_env_allow"`
	DispatchWait   string   `yaml:"dispatch_wait"`
}

const (
	logsDropPolicyKey  = "logs.drop_policy"
	pauseTimeoutKey    = "debug.pause_timeout"
	submitEnvAllowKey  = "run.submit_env_allow"
	dispatchWaitKey    = "run.dispatch_wait"
	logsDropPolicyWarn = "warn"
)

// hack: every concurrency key on the box scope reads the id, including a sort
// comparator, so it is read once per process; tests replace the function.
var boxIDSetting = sync.OnceValues(func() (string, error) {
	var s machineSettings
	path, _, err := userconfig.ReadDefault(userconfig.Machine, &s)
	if err != nil {
		return "", fmt.Errorf("%s machine.box_id: %w", path, err)
	}
	return strings.TrimSpace(s.BoxID), nil
})

// SetTestBoxID makes the box scope read id in place of machine.box_id until
// the test ends.
func SetTestBoxID(t interface{ Cleanup(func()) }, id string) {
	original := boxIDSetting
	t.Cleanup(func() { boxIDSetting = original })
	boxIDSetting = func() (string, error) { return id, nil }
}

var warnBoxIDOnce sync.Once

func boxHostID() string {
	id, err := boxIDSetting()
	if err != nil {
		// safety: a box-scoped key must not fail mid-plan; the hostname is the
		// documented default, and the warning names the broken setting.
		warnBoxIDOnce.Do(func() { slog.Warn("box scope falls back to the hostname", "err", err) })
	}
	if id != "" {
		return id
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "localhost"
}

// safety: an unreadable setting keeps the strict default, so a lost log line
// still fails the node rather than passing it silently.
func logsDropIsFatal() bool {
	var s logsSettings
	if _, _, err := userconfig.ReadDefault(userconfig.Logs, &s); err != nil {
		slog.Warn("log drop policy unreadable; dropped lines fail the node", "err", err)
		return true
	}
	return strings.TrimSpace(s.DropPolicy) != logsDropPolicyWarn
}

func pauseTimeout() time.Duration {
	var s debugSettings
	path, _, err := userconfig.ReadDefault(userconfig.Debug, &s)
	if err != nil {
		slog.Warn("debug pause timeout unreadable; using the default", "err", err, "default", defaultPauseTimeout)
		return defaultPauseTimeout
	}
	v := strings.TrimSpace(s.PauseTimeout)
	if v == "" {
		return defaultPauseTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		slog.Warn("debug pause timeout is not a positive duration; using the default",
			"file", path, "key", pauseTimeoutKey, "value", v, "default", defaultPauseTimeout)
		return defaultPauseTimeout
	}
	return d
}

func submitEnvAllowSetting() ([]string, string, error) {
	var s runSettings
	path, _, err := userconfig.ReadDefault(userconfig.Run, &s)
	if err != nil {
		return nil, path, err
	}
	return s.SubmitEnvAllow, path, nil
}

// safety: zero keeps the plan's own default and a negative value waits
// forever, the two meanings Options.DispatchWaitTimeout gives them.
func dispatchWaitSetting() (time.Duration, error) {
	var s runSettings
	path, _, err := userconfig.ReadDefault(userconfig.Run, &s)
	if err != nil {
		return 0, err
	}
	d, err := parseDispatchWaitTimeout(strings.TrimSpace(s.DispatchWait))
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", path, dispatchWaitKey, err)
	}
	return d, nil
}
