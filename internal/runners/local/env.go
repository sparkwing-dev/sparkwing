package local

import (
	"context"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// childEnv is the whole environment contract between a dispatcher and
// one node process, in one place so a reviewer can see every variable
// the child depends on without reading the child.
//
// It starts from base (the dispatcher's own environment) because a
// node body reads whatever the operator exported -- credentials,
// PATH, toolchain settings -- and then pins the variables that
// describe THIS node. Pinned values are appended, and os/exec takes
// the last value for a duplicate key, so a pinned value always beats
// an inherited one.
//
// Notably absent: SPARKWING_LOGS_URL. The child is passed --logs=
// explicitly empty so it writes node logs to the run's own files; an
// inherited value would be read only if the flag were missing.
func childEnv(ctx context.Context, base []string, cfg Config, req runner.Request) []string {
	env := make([]string, 0, len(base)+16)
	env = append(env, base...)

	set := func(k, v string) {
		if v == "" {
			return
		}
		env = append(env, k+"="+v)
	}

	set("SPARKWING_HOME", cfg.Home)
	set("SPARKWING_CONTROLLER_URL", cfg.ControllerURL)
	set("SPARKWING_CACHE_URL", cfg.CacheURL)
	set("SPARKWING_AGENT_TOKEN", cfg.AgentToken)
	set("SPARKWING_RUN_ID", req.RunID)
	set("SPARKWING_NODE_ID", req.NodeID)

	// safety: the dispatcher decodes the child's stdout as NDJSON log records,
	// so the child's renderer choice is not the operator's to make: a
	// pretty renderer would arrive as unparseable lines.
	set("SPARKWING_LOG_FORMAT", "json")

	// safety: what the node body sees through Runtime().Runner. "local" is the
	// operator-visible truth: the work ran on this machine, whatever
	// process boundary the dispatcher put around it.
	set("SPARKWING_RUNNER_NAME", "local")
	set("SPARKWING_RUNNER_TYPE", "local")
	set("SPARKWING_RUNNER_LABELS", strings.Join(cfg.Labels, ","))

	// safety: this is the child's authority to claim the descriptor. Without
	// it the child must not touch fd 3, which in any other process
	// belongs to whatever opened it.
	set(ParentLivenessFDEnv, strconv.Itoa(ParentLivenessFD))

	// safety: the run already holds an admission lease; a node process attaches
	// to it rather than opening a second one and double-charging the
	// host.
	if cfg.LeaseTokens != nil {
		lease, child := cfg.LeaseTokens(ctx)
		set(wingwire.LeaseTokenEnv, lease)
		set(wingwire.ChildLeaseTokenEnv, child)
	}

	// safety: run-level selections the child cannot infer from the run row.
	if cfg.DryRun {
		set("SPARKWING_DRY_RUN", "1")
	}
	set("SPARKWING_START_AT", cfg.StartAt)
	set("SPARKWING_STOP_AT", cfg.StopAt)

	return env
}
