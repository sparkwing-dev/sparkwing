package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type machineSettings struct {
	BoxID string `yaml:"box_id"`
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

func submitEnvAllowSetting() ([]string, string, error) {
	var s runSettings
	path, _, err := userconfig.ReadDefault(userconfig.Run, &s)
	if err != nil {
		return nil, path, err
	}
	return s.SubmitEnvAllow, path, nil
}
