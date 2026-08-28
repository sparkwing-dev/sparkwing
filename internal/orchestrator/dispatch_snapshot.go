package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const dispatchEnvelopeVersion = 1

type dispatchEnvelope struct {
	Version      int             `json:"version"`
	TypeName     string          `json:"type_name"`
	ScalarFields json.RawMessage `json:"scalar_fields,omitempty"`
}

var envAllowPrefixes = []string{
	"SPARKWING_",
	"GITHUB_",
}

var envAllowExact = map[string]bool{
	"KUBERNETES_SERVICE_HOST": true,
	"PATH":                    true,
	"HOME":                    true,
	"HOSTNAME":                true,
}

func (r *NodeExecutor) writeDispatchSnapshot(ctx context.Context, runID string, node *sparkwing.JobNode) error {
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
		Seq:              -1,
		CodeVersion:      codeVersion,
		RunnerLabels:     labelsBytes,
		EnvJSON:          envBytes,
		Workdir:          workdir,
		InputEnvelope:    envelope,
		SecretRedactions: redactions,
	})
}

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

var envDenyExact = map[string]bool{
	wingdclient.HostBinEnv: true,
}

func envAllowed(name string) bool {
	if envDenyExact[name] {
		return false
	}
	for _, p := range envAllowPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return envAllowExact[name]
}
