package local

import (
	"context"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// safety: the daemon authenticates an api.sock caller by its peer uid, and
// a bearer inherited from this process would be looked up on the daemon's
// writing handle instead, behind whatever it is doing.
var tokenEnvNames = []string{"SPARKWING_AGENT_TOKEN", "SPARKWING_TOKEN"}

func childEnv(ctx context.Context, base []string, cfg Config, req runner.Request) []string {
	env := make([]string, 0, len(base)+16)
	// safety: the dispatcher decides where this node's controller calls go, so
	// an inherited socket is dropped whichever way it decided. A pipeline run
	// from inside another run's node inherits that run's socket otherwise, and
	// dials a daemon that has never heard of it.
	drop := []string{wingwire.APISocketEnv}
	if cfg.APISocket != "" {
		drop = append(drop, tokenEnvNames...)
	}
	base = withoutEnv(base, drop)
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

func withoutEnv(base, names []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		drop := false
		for _, name := range names {
			if strings.HasPrefix(kv, name+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
