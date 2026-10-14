package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	k8srunner "github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podNode struct {
	entry                               pipelineentry.Command
	dir                                 string
	controllerURL, token, runID, nodeID string
	// safety: the accepted plan's hash for the node, which the pod's own plan of it must match.
	specHash string
	// safety: the claim's 1-based attempt ordinal, which decides before_run and after_run under retry_auto.
	attempt int
	// safety: a node a run host dispatched already ran its skip_if, memo and admission there, and its accepted
	// spec_hash is the one the run host recorded on its row.
	dispatched bool
	// safety: a dispatched node's caches use its own run's grant, since its host may serve other runs.
	cacheGrant string
	state      storage.StateStore
	artifacts  storage.ArtifactStore
	logger     *slog.Logger
}

// safety: the node's process gets the minimal environment and its own route
// bearer; the claim token stays in this process, which forwards the node's
// routes with it and masks it in every line the node writes.
func hostPodNode(ctx context.Context, p podNode) (nodehost.Result, error) {
	c := client.NewWithToken(p.controllerURL, nil, p.token)
	run, err := c.GetRunForExecution(ctx, p.runID)
	if err != nil {
		return nodehost.Result{}, fmt.Errorf("read run %s: %w", p.runID, err)
	}
	if p.dispatched {
		row, err := c.GetNode(ctx, p.runID, p.nodeID)
		if err != nil {
			return nodehost.Result{}, fmt.Errorf("read node %s: %w", p.nodeID, err)
		}
		p.specHash = row.SpecHash
		ctx = depcache.WithCacheGrant(ctx, p.cacheGrant)
	}
	masker := secrets.NewMasker()
	masker.Register(p.token)
	// safety: ahead of the routes and the node log, so a bad allow list leaves nothing to close.
	env, err := podProcessEnv(masker, p.logger)
	if err != nil {
		return nodehost.Result{}, err
	}
	routes, err := nodehost.Start(p.logger)
	if err != nil {
		return nodehost.Result{}, err
	}
	defer func() { //nolint:contextcheck // safety: Close bounds its own shutdown and takes no context.
		if err := routes.Close(); err != nil {
			p.logger.Warn("pod node: stop the node routes", "run_id", p.runID, "node_id", p.nodeID, "err", err)
		}
	}()
	claim, err := nodehost.NewClaim(p.controllerURL, p.token)
	if err != nil {
		// safety: only a claim token backs the forwarded routes, so a node under any other credential gets 502s.
		p.logger.Warn("pod node: the forwarded routes are off", "run_id", p.runID, "node_id", p.nodeID, "err", err)
		claim = nil
	}
	nlog := &podNodeLog{sink: openNodeLogSink(ctx, c, p.runID, p.nodeID, p.logger), out: os.Stdout, logger: p.logger}
	planSink, err := newPodPlanSink(ctx, p.runID, masker, os.Stderr)
	if err != nil {
		return nodehost.Result{}, err
	}
	defer planSink.end()
	flags := runflags.FromInvocation(run.Invocation)
	args, err := checkoutRunArgs(p.dir, run.Pipeline, run.Args, nil)
	if err != nil {
		return nodehost.Result{}, err
	}
	cfg := nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: p.entry, Dir: p.dir, Sink: planSink, Env: env},
		Pipeline: run.Pipeline, Args: args, Run: podRunContext(run),
		StartAt: flags.StartAt, StopAt: flags.StopAt,
		State: p.state, Artifacts: p.artifacts, Logger: p.logger,
		Attempts: &nodehost.Attempts{
			Routes: routes, Resolver: podSecrets{c: c, runID: p.runID}, Masker: masker, Claim: claim,
			OpenLog: func(context.Context, string, string) (nodehost.NodeLog, error) { return nlog, nil },
			Usage:   nodehost.RecordUsage(nil),
		},
	}
	var res nodehost.Result
	if p.dispatched {
		res = nodehost.HostDispatchedNode(ctx, cfg, p.nodeID, p.specHash)
	} else {
		slots := NewHTTPConcurrency(p.controllerURL, nil, p.token, store.DefaultConcurrencyLease)
		// safety: the node's concurrency group and memo entry run through the claim, which the controller binds to
		// this node; a memo hit's output and artifact manifest are the ones the controller picks for the claim's key.
		cfg.Admission = &nodehost.Admission{Slots: slots}
		cfg.Memo = &nodehost.Memo{Slots: slots, Claimed: true, NoCache: flags.NoCache, Source: func(ctx context.Context, in store.ClaimInputRequest, _, _ string) (nodehost.MemoSource, error) {
			src, data, err := c.ClaimInput(ctx, p.runID, p.nodeID, in)
			return nodehost.MemoSource{Output: data, ArtifactManifest: src.ArtifactManifest}, err
		}}
		res = nodehost.HostNode(ctx, cfg, p.nodeID, p.specHash, p.attempt)
	}
	nlog.seal(ctx, ctx.Err() != nil || res.Outcome == sparkwing.Cancelled)
	return res, nil
}

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
// dependency installs included: the minimal environment plus the dependency
// proxy settings the pod's Job or runner was given, so go, npm and pip use the
// cache's proxy and no runner credential reaches pipeline code or its hooks.
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
	out, finish, err := maskedInstallOutput(ctx, masker, dst)
	if err != nil {
		return nil, err
	}
	defer finish()
	return pipelineentry.Prepare(ctx, sparkwingDir, env, out)
}

// safety: npm's output goes through the node-host log sink, which holds back
// the tail of a long line's piece so a credential split across pieces is
// masked whole; finish runs once npm has exited, so the held tail is complete.
func maskedInstallOutput(ctx context.Context, masker *secrets.Masker, dst io.Writer) (io.Writer, func(), error) {
	sink, err := newPodPlanSink(ctx, "pipeline-install", masker, dst)
	if err != nil {
		return nil, nil, err
	}
	w := &nodehost.LineWriter{Sink: sink.LogSink, Level: nodehost.LevelWarn}
	return w, func() {
		w.Flush()
		sink.Flush()
	}, nil
}

func podEnvNames() []string {
	return slices.Concat(k8srunner.DependencyProxyEnvNames, k8srunner.NetworkEnvNames)
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
	p := (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipeline)
	switch {
	case p == nil:
		return nil, nil
	case len(p.Requires) == 0:
		// safety: a pipeline that names no requires takes the project's defaults, as the dispatcher's did.
		return slices.Clone(cfg.Defaults.Requires), nil
	}
	return slices.Clone(p.Requires), nil
}

// safety: the planning claim and every node's pod plan from one context, so their spec hashes compare.
func podRunContext(run *store.Run) nodeproto.RunContext {
	flags := runflags.FromInvocation(run.Invocation)
	rc := nodeproto.RunContext{
		RunID: run.ID, StartedAt: run.StartedAt, DryRun: flags.DryRun, NoCache: flags.NoCache,
		Git:     &nodeproto.Git{SHA: run.GitSHA, Branch: run.GitBranch, Repo: run.DeclaredRepo, RepoURL: run.RepoURL},
		Trigger: &nodeproto.Trigger{Source: protocolTriggerSource(run.TriggerSource), User: run.TriggerUser},
	}
	if pr := run.PullRequest; pr != nil {
		rc.Trigger.PullRequest = &nodeproto.PullRequest{
			Number: pr.Number, Action: pr.Action, BaseRef: pr.BaseRef, BaseSHA: pr.BaseSHA, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA,
		}
	}
	return rc
}

// safety: the node protocol knows four sources, and a run's recorded source is
// any of the surfaces that start runs (cli, api, retry, await-pipeline, a
// working-tree snapshot), so each is named by the protocol source it means;
// one the protocol does not know would fail the plan.
func protocolTriggerSource(recorded string) string {
	switch recorded {
	case "schedule", "push", "webhook":
		return recorded
	case "github":
		return "webhook"
	}
	return "manual"
}

// safety: one log for every process of the attempt, sealed once by
// hostPodNode, because a second segment sink would renumber the first's. A
// controller that keeps no segments gets the container's log instead.
type podNodeLog struct {
	sink   *nodeLogSink
	out    io.Writer
	logger *slog.Logger
	mu     sync.Mutex
	seq    int64
	lost   int64
}

// safety: the host numbers its lines on one stream, so the seal can say how many it lost.
const podLogStream = "host"

func (l *podNodeLog) Emit(rec sparkwing.LogRecord) {
	line, err := json.Marshal(rec)
	if err == nil {
		line = append(line, '\n')
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case err != nil:
	case l.sink != nil:
		l.seq++
		err = l.sink.sink.Append(line, podLogStream, l.seq, l.seq)
	default:
		_, err = l.out.Write(line)
	}
	if err != nil {
		// safety: warned once, since a log that refuses one line refuses the rest.
		if l.lost == 0 {
			l.logger.Warn("pod node: node log lines lost", "err", err)
		}
		l.lost++
	}
}

func (l *podNodeLog) Close() error { return nil }

func (l *podNodeLog) RecordLoss(lines int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lost += int64(lines)
}

// safety: a log that lost lines seals as incomplete, so no reader takes the prefix for the whole log.
func (l *podNodeLog) seal(ctx context.Context, stopped bool) {
	if l.sink == nil {
		return
	}
	l.mu.Lock()
	l.sink.sink.WriterSealed(podLogStream, l.seq, l.lost)
	l.mu.Unlock()
	l.sink.close(ctx, stopped)
}

// safety: plan code's output goes to out, the container's stderr, through a
// node-host log sink, which masks a value even across the session's split of
// a long line; the planning claim has no node log. Call end once the plan
// process ends.
func newPodPlanSink(ctx context.Context, runID string, masker *secrets.Masker, out io.Writer) (podPlanSink, error) {
	log := &podPlanLog{out: out}
	sink, err := nodehost.NewLogSink(ctx, nodehost.LogSinkConfig{
		RunID: runID, NodeID: store.PlanNodeID, Log: log, State: podPlanState{}, Masker: masker,
	})
	return podPlanSink{LogSink: sink, log: log}, err
}

type podPlanSink struct {
	*nodehost.LogSink
	log *podPlanLog
}

// safety: ending drops the held tail of a split line and every later line
// rather than flushing them, since a queue the session abandoned can still
// deliver the rest of a secret whose start a flush would print.
func (s podPlanSink) end() { s.log.ended.Store(true) }

type podPlanLog struct {
	mu    sync.Mutex
	out   io.Writer
	ended atomic.Bool
}

func (l *podPlanLog) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended.Load() {
		return
	}
	if _, err := fmt.Fprintln(l.out, rec.Msg); err != nil {
		return
	}
}

// safety: plan code has no node row, so its annotations and summaries are dropped.
type podPlanState struct{}

func (podPlanState) AppendNodeAnnotation(context.Context, string, string, string) error { return nil }
func (podPlanState) AppendStepAnnotation(context.Context, string, string, string, string) error {
	return nil
}
func (podPlanState) SetNodeSummary(context.Context, string, string, string) error         { return nil }
func (podPlanState) SetStepSummary(context.Context, string, string, string, string) error { return nil }

// safety: a secret is released only through the claim, under the plan's
// declaration, as the controller rules for the claim's kind.
type podSecrets struct {
	c     *client.Client
	runID string
}

func (s podSecrets) Resolve(ctx context.Context, name string) (string, bool, error) {
	sec, err := s.c.GetSecretForRun(ctx, name, s.runID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", false, secrets.ErrSecretMissing
	case err != nil:
		return "", false, err
	}
	return sec.Value, sec.Masked, nil
}
