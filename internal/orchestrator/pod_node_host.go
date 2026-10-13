package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podNode struct {
	binary, dir                         string
	controllerURL, token, runID, nodeID string
	// safety: the accepted plan's hash for the node, which the pod's own plan of it must match.
	specHash string
	// safety: the claim's 1-based attempt ordinal, which decides before_run and after_run under retry_auto.
	attempt int
	// safety: a node a run host dispatched already ran its skip_if, memo and admission there, and its accepted
	// spec_hash is the one the run host recorded on its row.
	dispatched bool
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
	}
	masker := secrets.NewMasker()
	masker.Register(p.token)
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
	cfg := nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: p.binary, Dir: p.dir, Sink: planSink},
		Pipeline: run.Pipeline, Args: run.Args, Run: podRunContext(run),
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
		// this node; a memo hit's output is the one the controller picks for the claim's key.
		cfg.Admission = &nodehost.Admission{Slots: slots}
		cfg.Memo = &nodehost.Memo{Slots: slots, Claimed: true, Output: func(ctx context.Context, in store.ClaimInputRequest, _, _ string) ([]byte, error) {
			_, data, err := c.ClaimInput(ctx, p.runID, p.nodeID, in)
			return data, err
		}}
		res = nodehost.HostNode(ctx, cfg, p.nodeID, p.specHash, p.attempt)
	}
	nlog.seal(ctx, ctx.Err() != nil || res.Outcome == sparkwing.Cancelled)
	return res, nil
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
	if p := (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipeline); p != nil {
		return slices.Clone(p.Requires), nil
	}
	return nil, nil
}

// safety: the planning claim and every node's pod plan from one context, so their spec hashes compare.
func podRunContext(run *store.Run) nodeproto.RunContext {
	return nodeproto.RunContext{
		RunID: run.ID, StartedAt: run.StartedAt,
		Git:     &nodeproto.Git{SHA: run.GitSHA, Branch: run.GitBranch, Repo: run.DeclaredRepo, RepoURL: run.RepoURL},
		Trigger: &nodeproto.Trigger{Source: cmp.Or(run.TriggerSource, "manual")},
	}
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
		return "", false, secretsource.ErrMissing
	case err != nil:
		return "", false, err
	}
	return sec.Value, sec.Masked, nil
}
