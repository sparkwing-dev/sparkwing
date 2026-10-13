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
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
	attempt   int
	state     storage.StateStore
	artifacts storage.ArtifactStore
	logger    *slog.Logger
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
	res := nodehost.HostNode(ctx, nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: p.binary, Dir: p.dir, Sink: podPlanSink{masker: masker}},
		Pipeline: run.Pipeline, Args: run.Args, Run: podRunContext(run),
		State: p.state, Artifacts: p.artifacts, Logger: p.logger,
		// safety: the node's concurrency group runs through the claim, which the controller binds to this node.
		Admission: &nodehost.Admission{Slots: NewHTTPConcurrency(p.controllerURL, nil, p.token, store.DefaultConcurrencyLease)},
		Attempts: &nodehost.Attempts{
			Routes: routes, Resolver: podSecrets{c: c, runID: p.runID}, Masker: masker, Claim: claim,
			OpenLog: func(context.Context, string, string) (nodehost.NodeLog, error) { return nlog, nil },
			Usage:   nodehost.RecordUsage(nil),
		},
	}, p.nodeID, p.specHash, p.attempt)
	if nlog.sink != nil {
		nlog.sink.close(ctx, ctx.Err() != nil || res.Outcome == sparkwing.Cancelled)
	}
	return res, nil
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
	lost   bool
}

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
		err = l.sink.sink.Append(line, "", 0, 0)
	default:
		_, err = l.out.Write(line)
	}
	// safety: warned once, since a log that refuses one line refuses the rest.
	if err != nil && !l.lost {
		l.lost = true
		l.logger.Warn("pod node: node log lines lost", "err", err)
	}
}

func (l *podNodeLog) Close() error { return nil }

// safety: plan code's output goes to the container's stderr, masked, since
// the planning claim has no node log.
type podPlanSink struct{ masker *secrets.Masker }

func (s podPlanSink) Text(_, line string) { fmt.Fprintln(os.Stderr, s.masker.Mask(line)) }

func (s podPlanSink) Record(rec nodeproto.LogRecord) { fmt.Fprintln(os.Stderr, s.masker.Mask(rec.Msg)) }

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
