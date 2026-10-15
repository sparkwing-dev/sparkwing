package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: a proxy URL can carry a credential, so each one's password, and the
// whole value, is masked before any process of the node runs.
func podSessionEnv(masker *secrets.Masker) []string {
	for _, name := range podEnvNames() {
		if v, ok := os.LookupEnv(name); ok {
			maskProxyCredentials(masker, v)
		}
	}
	return PodBaseEnv()
}

// safety: every pipeline process a runner starts, its plan, closure evals, nodes and dependency installs, gets
// PodBaseEnv plus the operator's allow-listed names from this one function, with what needs masking registered;
// a value plan code reads that differed between planning and the node would fail the node with plan_drift.
func podProcessEnv(masker *secrets.Masker, logger *slog.Logger) ([]string, error) {
	base := podSessionEnv(masker)
	operator, err := operatorAllowedEnv(os.Environ(), base, masker, logger)
	if err != nil {
		return nil, err
	}
	return append(base, operator...), nil
}

// PodBaseEnv is what a runner gives every process of a pipeline it hosts, its
// dependency installs included: the minimal environment plus the network
// proxy and CA settings needed to reach external services. Custom registry
// settings use the operator's run.submit_env_allow list.
func PodBaseEnv() []string {
	return engineenv.Minimal(podEnvNames()...)
}

// PrepareTypeScriptOnRunner installs and returns sparkwingDir's TypeScript
// entry on a runner: npm runs with the environment a node process gets,
// [PodBaseEnv] plus the operator's allow-listed names, and its output reaches
// stderr only after the credentials and long values that environment carries
// are masked.
func PrepareTypeScriptOnRunner(ctx context.Context, sparkwingDir string) (pipelineentry.Command, error) {
	return prepareTypeScriptOnRunner(ctx, sparkwingDir, os.Stderr)
}

func prepareTypeScriptOnRunner(ctx context.Context, sparkwingDir string, dst io.Writer) (pipelineentry.Command, error) {
	masker := secrets.NewMasker()
	// safety: the allow list is operator-trusted config a private registry or a tool's install may need.
	env, err := podProcessEnv(masker, slog.Default())
	if err != nil {
		return nil, err
	}
	out, err := maskedInstallOutput(ctx, masker, dst)
	if err != nil {
		return nil, err
	}
	defer out.Flush()
	return pipelineentry.Prepare(ctx, sparkwingDir, env, out)
}

// safety: npm's output is masked as one stream, so a credential split across
// a long line's pieces is masked whole; flush it once npm has exited.
func maskedInstallOutput(ctx context.Context, masker *secrets.Masker, dst io.Writer) (*nodehost.LineWriter, error) {
	sink, err := newPodPlanSink(ctx, "pipeline-install", masker, dst)
	if err != nil {
		return nil, err
	}
	return nodehost.NewLineWriter(sink, nodehost.LevelWarn, masker), nil
}

func podEnvNames() []string {
	var names []string
	for _, rule := range procenv.Registry {
		if rule.RemoteBase && !rule.Prefix {
			names = append(names, rule.Name)
		}
	}
	return names
}

// safety: the operator's run.submit_env_allow names and prefixes reach the node from this runner's environment, as
// AWS_PROFILE or DOCKER_HOST must; an engine variable or a credential-shaped name or value never does, and a name
// base sets keeps its value. A neutral name can still hold a bare token, so a value long enough to be one is masked;
// shorter config such as a region stays readable, since masking it would cut it out of every line that names it.
func operatorAllowedEnv(env, base []string, masker *secrets.Masker, logger *slog.Logger) ([]string, error) {
	names, prefixes, err := submissionEnvironmentAllowList()
	if err != nil {
		return nil, err
	}
	var out, engine, credential []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !operatorListed(key, names, prefixes) || slices.ContainsFunc(base, func(kv string) bool {
			return strings.HasPrefix(kv, key+"=")
		}) {
			continue
		}
		switch {
		case engineenv.Owned(key):
			engine = append(engine, key)
		case submissionEnvironmentCredentialName(key, value) || envredact.CredentialValue(value) || envredact.RedactValue(value) != value:
			credential = append(credential, key)
		default:
			if len(value) >= operatorEnvMaskMinLen {
				masker.Register(value)
			}
			out = append(out, entry)
		}
	}
	if (len(engine) > 0 || len(credential) > 0) && logger != nil {
		logger.Warn("pod node: dropped allow-listed names the node may not inherit",
			"engine_names", strings.Join(engine, ","), "credential_names", strings.Join(credential, ","),
			"allow_key", submitEnvAllowKey)
	}
	return out, nil
}

const operatorEnvMaskMinLen = 12

func operatorListed(key string, names map[string]bool, prefixes []string) bool {
	if names[key] {
		return true
	}
	return slices.ContainsFunc(prefixes, func(prefix string) bool { return prefix != "" && strings.HasPrefix(key, prefix) })
}

// safety: the whole value and each ','/'|' member are masked whole. The userinfo (after any scheme://, up to the
// last '@') and its password, raw and percent-decoded, are taken from each member and from the unsplit value, so a
// password holding a ',', '|' or '@' is masked whole. A value with no '@' carries no credential, and GOPROXY's direct
// and off keywords stay readable.
func maskProxyCredentials(masker *secrets.Masker, value string) {
	if !strings.Contains(value, "@") {
		return
	}
	masker.Register(value)
	maskUserinfo(masker, value)
	for _, member := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '|' }) {
		if member != "direct" && member != "off" {
			masker.Register(member)
		}
		maskUserinfo(masker, member)
	}
}

func maskUserinfo(masker *secrets.Masker, s string) {
	_, rest, ok := strings.Cut(s, "://")
	if !ok {
		rest = s
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return
	}
	userinfo := rest[:at]
	parts := []string{userinfo}
	if _, password, ok := strings.Cut(userinfo, ":"); ok {
		parts = append(parts, password)
	}
	for _, raw := range parts {
		if raw == "" {
			continue
		}
		masker.Register(raw)
		if decoded, err := url.PathUnescape(raw); err == nil {
			masker.Register(decoded)
		}
	}
}

// PipelineRequires is the placement labels the pipeline's entry in the
// sparkwing.yaml found from dir upward requires; none when there is no such
// file or entry. The host joins them to the plan, which plan code cannot
// drop them from.
func PipelineRequires(dir, pipeline string) ([]string, error) {
	path, cfg, err := projectconfig.Discover(dir)
	if err != nil {
		return nil, fmt.Errorf("read the pipeline's placement: %w", err)
	}
	if path == "" || cfg == nil {
		return nil, nil
	}
	if p := cfg.Registry().Find(pipeline); p != nil {
		return slices.Clone(p.Requires), nil
	}
	if cfg.Defaults != nil {
		return slices.Clone(cfg.Defaults.Requires), nil
	}
	return nil, nil
}

func protocolTriggerSource(recorded string) string {
	switch recorded {
	case "schedule", "push", "webhook":
		return recorded
	case "github":
		return "webhook"
	}
	return "manual"
}

func newPodPlanSink(ctx context.Context, runID string, masker *secrets.Masker, out io.Writer) (*nodehost.LogSink, error) {
	return nodehost.NewLogSink(ctx, nodehost.LogSinkConfig{
		RunID: runID, NodeID: store.PlanNodeID, Log: &podPlanLog{out: out}, Masker: masker,
	})
}

type podPlanLog struct {
	mu  sync.Mutex
	out io.Writer
}

func (l *podPlanLog) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintln(l.out, rec.Msg)
}
