package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/envredact"
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

	env := collectDispatchEnv(ctx, node, runID, run)
	envBytes, err := json.Marshal(env.values)
	if err != nil {
		return fmt.Errorf("marshal env: %w", err)
	}
	var redactedBytes []byte
	if len(env.redactedKeys) > 0 {
		redactedBytes, _ = json.Marshal(env.redactedKeys)
	}
	redactions += env.masked

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
		RedactedKeys:     redactedBytes,
	})
}

type dispatchEnv struct {
	values       map[string]string
	redactedKeys []string
	masked       int
}

func collectDispatchEnv(ctx context.Context, node *sparkwing.JobNode, runID string, run *store.Run) dispatchEnv {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		if !envAllowed(k) {
			if envDenyExact[k] {
				out[k] = v
			}
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
	return redactDispatchEnv(ctx, out)
}

func redactDispatchEnv(ctx context.Context, env map[string]string) dispatchEnv {
	got := dispatchEnv{values: env}
	masker := secrets.MaskerFromContext(ctx)
	for k, v := range env {
		if envDenyExact[k] {
			delete(env, k)
			got.redactedKeys = append(got.redactedKeys, k)
			continue
		}
		// safety: drop credential-shaped names and values; the snapshot must not carry a bearer or a DSN password.
		if envredact.CredentialName(k) || envredact.CredentialValue(v) {
			delete(env, k)
			got.redactedKeys = append(got.redactedKeys, k)
			continue
		}
		if r := envredact.RedactValue(v); r != v {
			env[k] = r
			v = r
			got.masked++
		}
		if masker == nil {
			continue
		}
		if m := masker.Mask(v); m != v {
			env[k] = m
			got.masked++
		}
	}
	sort.Strings(got.redactedKeys)
	return got
}

var envDenyExact = map[string]bool{
	wingdclient.HostBinEnv:               true,
	"SPARKWING_AGENT_TOKEN":              true,
	"SPARKWING_CONTROLLER_URL":           true,
	"SPARKWING_LOGS_URL":                 true,
	"SPARKWING_CACHE_TOKEN":              true,
	remoteExecutionCapabilityEnv:         true,
	remoteExecutionCapabilityInputEnv:    true,
	remoteBrokeredArtifactEnv:            true,
	remoteBrokeredClaimEnv:               true,
	"SPARKWING_NODE_CLAIM_HOLDER":        true,
	"SPARKWING_NODE_CLAIM_GENERATION":    true,
	"SPARKWING_NODE_CLAIM_MEMBERSHIP":    true,
	"SPARKWING_NODE_CLAIM_RESERVATION":   true,
	"SPARKWING_TRIGGER_CLAIM_GENERATION": true,
	"SPARKWING_TRIGGER_GENERATION":       true,
	"SPARKWING_ATTEMPT_ORDINAL":          true,
	"SPARKWING_FLEET_PARENT_GUARD":       true,
	"SPARKWING_FLEET_PARENT_TOKEN":       true,
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
