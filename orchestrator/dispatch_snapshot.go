package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Bumped when the persisted envelope shape changes incompatibly.
// Replay rejects unknown versions loudly.
const dispatchEnvelopeVersion = 1

// dispatchEnvelope is the JSON persisted into
// node_dispatches.input_envelope_json. ScalarFields is the post-Ref-
// resolution job marshal; replay rehydrates Refs against the original
// run.
type dispatchEnvelope struct {
	Version      int             `json:"version"`
	TypeName     string          `json:"type_name"`
	ScalarFields json.RawMessage `json:"scalar_fields,omitempty"`
}

// Env-var name prefixes captured into node_dispatches.env_json. Keeps
// operator credentials off disk while preserving everything needed to
// reproduce RuntimeConfig and git context.
var envAllowPrefixes = []string{
	"SPARKWING_",
	"GITHUB_",
}

// envAllowExact pins names that don't fit a prefix but are needed to
// reproduce dispatch-time behavior (KUBERNETES_SERVICE_HOST drives
// RuntimeConfig.IsLocal).
var envAllowExact = map[string]bool{
	"KUBERNETES_SERVICE_HOST": true,
	"PATH":                    true,
	"HOME":                    true,
	"HOSTNAME":                true,
}

// writeDispatchSnapshot captures the dispatch frame for one
// (run, node, attempt). Called before BeforeRun so replays re-run
// hooks and pick up rotated secrets lazily.
func (r *InProcessRunner) writeDispatchSnapshot(ctx context.Context, runID string, node *sparkwing.JobNode) error {
	scalar, err := json.Marshal(node.Job())
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	redactions := 0
	if m := secrets.MaskerFromContext(ctx); m != nil {
		before := strings.Count(string(scalar), "***")
		masked := m.Mask(string(scalar))
		after := strings.Count(masked, "***")
		if after > before {
			redactions = after - before
		}
		scalar = []byte(masked)
	}
	envelope, err := json.Marshal(dispatchEnvelope{
		Version:      dispatchEnvelopeVersion,
		TypeName:     fmt.Sprintf("%T", node.Job()),
		ScalarFields: scalar,
	})
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	// Run row supplies SPARKWING_*/GITHUB_* keys that aren't in
	// os.Environ() under laptop dispatch, plus code_version.
	var run *store.Run
	if got, _ := r.backends.State.GetRun(ctx, runID); got != nil {
		run = got
	}

	envBytes, err := json.Marshal(collectDispatchEnv(node, runID, run))
	if err != nil {
		return fmt.Errorf("marshal env: %w", err)
	}

	var labelsBytes []byte
	if labels := node.RequiresLabels(); len(labels) > 0 {
		labelsBytes, _ = json.Marshal(labels)
	}

	workdir := sparkwing.CurrentRuntime().WorkDir
	if workdir == "" {
		if d, err := os.Getwd(); err == nil {
			workdir = d
		}
	}

	var codeVersion string
	if run != nil {
		codeVersion = run.GitSHA
	}

	return r.backends.State.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID:            runID,
		NodeID:           node.ID(),
		Seq:              -1, // store assigns next attempt
		CodeVersion:      codeVersion,
		RunnerLabels:     labelsBytes,
		EnvJSON:          envBytes,
		Workdir:          workdir,
		InputEnvelope:    envelope,
		SecretRedactions: redactions,
	})
}

// collectDispatchEnv layers env (last-wins): allowlisted os.Environ,
// synthesized run-context vars, then node.EnvMap. run may be nil.
func collectDispatchEnv(node *sparkwing.JobNode, runID string, run *store.Run) map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		if !envAllowed(k) {
			continue
		}
		out[k] = v
	}
	// Empty values are skipped so a partial run row doesn't introduce
	// empty SPARKWING_* entries.
	stamp := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	stamp("SPARKWING_RUN_ID", runID)
	stamp("SPARKWING_NODE_ID", node.ID())
	if run != nil {
		stamp("SPARKWING_BRANCH", run.GitBranch)
		stamp("SPARKWING_COMMIT", run.GitSHA)
		stamp("SPARKWING_TRIGGER_SOURCE", run.TriggerSource)
		stamp("SPARKWING_REPO", run.Repo)
		if run.GithubOwner != "" && run.GithubRepo != "" {
			stamp("GITHUB_REPOSITORY", run.GithubOwner+"/"+run.GithubRepo)
		}
		stamp("GITHUB_SHA", run.GitSHA)
		stamp("GITHUB_REF_NAME", run.GitBranch)
	}
	for k, v := range node.EnvMap() {
		out[k] = v
	}
	return out
}

func envAllowed(name string) bool {
	for _, p := range envAllowPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return envAllowExact[name]
}
