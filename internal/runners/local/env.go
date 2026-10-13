package local

import (
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func childEnv(ctx context.Context, base []string, cfg Config, req runner.Request) []string {
	env := make([]string, 0, len(base)+16)
	// safety: the node runs authored init code, so it inherits nothing the engine owns: not a bearer or secrets key,
	// nor an outer run's socket, --only or run-handle path. cfg sets what it needs; over the API socket that is no
	// bearer, because the daemon takes the connection's peer uid as the principal.
	env = append(env, engineenv.Strip(base)...)

	set := func(k, v string) {
		if v == "" {
			return
		}
		env = append(env, k+"="+v)
	}

	set("SPARKWING_HOME", cfg.Home)
	set("SPARKWING_CONTROLLER_URL", cfg.ControllerURL)
	set("SPARKWING_CACHE_URL", cfg.CacheURL)
	set(wingwire.APISocketEnv, cfg.APISocket)
	if cfg.APISocket == "" {
		set("SPARKWING_AGENT_TOKEN", cfg.AgentToken)
	}
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
	if cfg.NoCache {
		set("SPARKWING_NO_CACHE", "1")
	}
	set("SPARKWING_START_AT", cfg.StartAt)
	set("SPARKWING_STOP_AT", cfg.StopAt)

	return env
}
